package gitx

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cduggn/gaffer/internal/work"
)

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	for _, args := range [][]string{{"init", "-q"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		if _, err := Git(dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func print(t *testing.T, dir string) work.Fingerprint {
	t.Helper()
	fp, err := Fingerprint(dir)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

func TestFingerprintTracksEveryChange(t *testing.T) {
	dir := repo(t)
	clean := print(t, dir)
	if clean.Dirty != "" {
		t.Fatalf("clean tree has dirty digest %q", clean.Dirty)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "one")
	untracked := print(t, dir)
	if untracked == clean {
		t.Fatal("an untracked file did not change the fingerprint")
	}
	write("a.txt", "two")
	if print(t, dir) == untracked {
		t.Fatal("editing an untracked file did not change the fingerprint")
	}
	Git(dir, "add", "a.txt")
	Git(dir, "commit", "-qm", "a")
	committed := print(t, dir)
	if committed.Dirty != "" || committed.Commit == clean.Commit {
		t.Fatalf("after commit fingerprint = %+v", committed)
	}
	write("a.txt", "three")
	if print(t, dir) == committed {
		t.Fatal("a tracked edit did not change the fingerprint")
	}
	write("a.txt", "two")
	if print(t, dir) != committed {
		t.Fatal("reverting the edit did not restore the fingerprint")
	}
}

func TestAddWorktreeIsIdempotent(t *testing.T) {
	dir := repo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	for i := 0; i < 2; i++ {
		if err := AddWorktree(dir, wt, "gaffer/x", "HEAD"); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	main, err := MainRoot(wt)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	if got, _ := filepath.EvalSymlinks(main); got != want {
		t.Fatalf("MainRoot(worktree) = %s, want %s", got, want)
	}
}
