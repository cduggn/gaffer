package beads

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// BD shells out to the bd CLI against one repository's beads database.
type BD struct {
	Bin  string // defaults to "bd"
	Repo string // the main checkout; bd is always run from here, never a worktree
}

func (b BD) run(actor string, args ...string) ([]byte, error) {
	bin := b.Bin
	if bin == "" {
		bin = "bd"
	}
	full := append([]string{"-C", b.Repo, "--json", "--quiet"}, args...)
	if actor != "" {
		full = append(full, "--actor", actor)
	}
	cmd := exec.Command(bin, full...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if strings.Contains(msg, "already claimed by") {
			return nil, fmt.Errorf("%w: %s", ErrClaimed, msg)
		}
		return nil, fmt.Errorf("bd %s: %v: %s", strings.Join(args, " "), err, msg)
	}
	return out.Bytes(), nil
}

func decodeList(raw []byte) ([]Issue, error) {
	var out []Issue
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decoding bd output: %w", err)
	}
	return out, nil
}

// Ready lists unblocked open issues in bd's priority order.
func (b BD) Ready(labels []string) ([]Issue, error) {
	// Epics are containers; their children are the work.
	args := []string{"ready", "--limit", "0", "--exclude-type", "epic"}
	if len(labels) > 0 {
		args = append(args, "--label", strings.Join(labels, ","))
	}
	raw, err := b.run("", args...)
	if err != nil {
		return nil, err
	}
	return decodeList(raw)
}

func (b BD) Show(id string) (Issue, error) {
	raw, err := b.run("", "show", id)
	if err != nil {
		return Issue{}, err
	}
	list, err := decodeList(raw)
	if err != nil {
		return Issue{}, err
	}
	if len(list) != 1 {
		return Issue{}, fmt.Errorf("bd show %s returned %d issues", id, len(list))
	}
	return list[0], nil
}

// Claim takes the issue for actor atomically. Re-claiming your own issue is
// a no-op; another actor's claim returns ErrClaimed.
func (b BD) Claim(id, actor string) error {
	_, err := b.run(actor, "update", id, "--claim")
	return err
}

// Release returns the issue to the ready queue, only while actor holds it.
func (b BD) Release(id, actor string) error {
	_, err := b.run(actor, "unclaim", id, "--if-assignee", actor)
	return err
}

func (b BD) Heartbeat(id, actor string) error {
	_, err := b.run(actor, "heartbeat", id)
	return err
}

func (b BD) Comment(id, actor, text string) error {
	_, err := b.run(actor, "comments", "add", id, text)
	return err
}

func (b BD) SetStatus(id, actor, status string) error {
	_, err := b.run(actor, "update", id, "--status", status)
	return err
}

func (b BD) Close(id, actor, reason string) error {
	_, err := b.run(actor, "close", id, "--reason", reason)
	return err
}
