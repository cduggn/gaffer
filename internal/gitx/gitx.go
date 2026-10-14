// Package gitx is the thin boundary between gaffer and git.
package gitx

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cduggn/gaffer/internal/work"
)

// Git runs git in dir and returns trimmed stdout.
func Git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// Root is the top of the working tree containing dir.
func Root(dir string) (string, error) { return Git(dir, "rev-parse", "--show-toplevel") }

// CommonDir is the .git directory shared by every worktree of the repo, which
// is where gaffer keeps run state so no worktree diff ever includes it.
func CommonDir(dir string) (string, error) {
	d, err := Git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.Clean(d), nil
}

// MainRoot is the primary checkout, even when dir is a linked worktree.
func MainRoot(dir string) (string, error) {
	common, err := CommonDir(dir)
	if err != nil {
		return "", err
	}
	return filepath.Dir(common), nil
}

func Head(dir string) (string, error) { return Git(dir, "rev-parse", "HEAD") }

// Fingerprint digests HEAD plus every tracked change and untracked file, so
// any edit after a check makes that check's evidence stale.
func Fingerprint(dir string) (work.Fingerprint, error) {
	head, err := Head(dir)
	if err != nil {
		return work.Fingerprint{}, err
	}
	diff := exec.Command("git", "diff", "--binary", "HEAD")
	diff.Dir = dir
	patch, err := diff.Output()
	if err != nil {
		return work.Fingerprint{}, fmt.Errorf("git diff: %w", err)
	}
	untracked, err := Git(dir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return work.Fingerprint{}, err
	}
	if len(patch) == 0 && untracked == "" {
		return work.Fingerprint{Commit: head}, nil
	}
	h := sha256.New()
	h.Write(patch)
	for _, name := range strings.Split(untracked, "\x00") {
		if name == "" {
			continue
		}
		fmt.Fprintf(h, "\x00%s\x00", name)
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			fmt.Fprintf(h, "unreadable:%v", err)
			continue
		}
		_, _ = io.Copy(h, f)
		f.Close()
	}
	return work.Fingerprint{Commit: head, Dirty: hex.EncodeToString(h.Sum(nil))}, nil
}

// AddWorktree creates (or adopts, if it already exists) a worktree at path on
// branch, starting from base. Re-running it converges on the same state.
func AddWorktree(repo, path, branch, base string) error {
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		got, err := Git(path, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return err
		}
		if got != branch {
			return fmt.Errorf("worktree %s is on %s, expected %s", path, got, branch)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := Git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		_, err = Git(repo, "worktree", "add", path, branch)
		return err
	}
	_, err := Git(repo, "worktree", "add", "-b", branch, path, base)
	return err
}
