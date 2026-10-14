package verify

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/cduggn/gaffer/internal/work"
)

const registry = `{
  "defaults": ["ok"],
  "verifiers": [
    {"id": "ok", "claim": "passes", "command": ["sh", "-c", "echo fine"], "timeout_seconds": 5},
    {"id": "bad", "claim": "fails", "command": ["sh", "-c", "exit 3"], "timeout_seconds": 5},
    {"id": "slow", "claim": "hangs", "command": ["sh", "-c", "sleep 30"], "timeout_seconds": 0.2},
    {"id": "missing", "claim": "absent binary", "command": ["/nonexistent/gaffer-check"], "timeout_seconds": 5},
    {"id": "writer", "claim": "edits the tree", "command": ["sh", "-c", "echo x >> tree.txt"], "timeout_seconds": 5}
  ]
}`

// contentPrint stands in for git: the tree is whatever tree.txt holds.
func contentPrint(dir string) (work.Fingerprint, error) {
	b, _ := os.ReadFile(filepath.Join(dir, "tree.txt"))
	return work.Fingerprint{Commit: "c", Dirty: string(b) + "____________"}, nil
}

func statuses(v work.Verification) map[string]work.Status {
	out := map[string]work.Status{}
	for _, c := range v.Checks {
		out[c.ID] = c.Status
	}
	return out
}

func TestRun(t *testing.T) {
	reg, err := Parse([]byte(registry))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		ids    []string
		status work.Status
		checks map[string]work.Status
	}{
		{[]string{"ok"}, work.Pass, map[string]work.Status{"ok": work.Pass}},
		{[]string{"ok", "bad"}, work.Fail, map[string]work.Status{"ok": work.Pass, "bad": work.Fail}},
		{[]string{"bad", "slow"}, work.BlockedStatus, map[string]work.Status{"bad": work.Fail, "slow": work.BlockedStatus}},
		{[]string{"missing"}, work.BlockedStatus, map[string]work.Status{"missing": work.BlockedStatus}},
		{[]string{"unregistered"}, work.BlockedStatus, map[string]work.Status{"unregistered": work.BlockedStatus}},
		{[]string{"writer"}, work.BlockedStatus, map[string]work.Status{"writer": work.Pass, "tree-unchanged": work.BlockedStatus}},
	}
	for _, c := range cases {
		dir := t.TempDir()
		start := time.Now()
		v, err := Run(context.Background(), reg, dir, filepath.Join(dir, "logs"), c.ids, contentPrint)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status != c.status || !reflect.DeepEqual(statuses(v), c.checks) {
			t.Errorf("%v: status %s checks %v; want %s %v", c.ids, v.Status, statuses(v), c.status, c.checks)
		}
		if time.Since(start) > 5*time.Second {
			t.Errorf("%v: timeout did not kill the check", c.ids)
		}
	}
}

func TestRunWritesLogs(t *testing.T) {
	reg, _ := Parse([]byte(registry))
	dir := t.TempDir()
	v, _ := Run(context.Background(), reg, dir, filepath.Join(dir, "logs"), []string{"ok"}, contentPrint)
	got, err := os.ReadFile(v.Checks[0].Log)
	if err != nil || string(got) != "fine\n" {
		t.Fatalf("log = %q, %v", got, err)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		`{"verifiers":[{"id":"a","command":["x"],"timeout_seconds":1},{"id":"a","command":["x"],"timeout_seconds":1}]}`: ".gaffer/verifiers.json: duplicate verifier id a",
		`{"verifiers":[{"id":"a","command":[],"timeout_seconds":1}]}`:                                                   ".gaffer/verifiers.json: verifier a has no command",
		`{"verifiers":[{"id":"a","command":["x"],"timeout_seconds":0}]}`:                                                ".gaffer/verifiers.json: verifier a needs a positive timeout_seconds",
		`{"defaults":["b"],"verifiers":[]}`:                                                                             ".gaffer/verifiers.json: default b is not registered",
		`{"verifiers":[{"id":"-a","command":["x"],"timeout_seconds":1}]}`:                                               ".gaffer/verifiers.json: verifier 0 has invalid id \"-a\"",
	}
	for in, want := range cases {
		if _, err := Parse([]byte(in)); err == nil || err.Error() != want {
			t.Errorf("Parse(%s) = %v, want %q", in, err, want)
		}
	}
}

func TestWeakened(t *testing.T) {
	base, _ := Parse([]byte(`{"defaults":["a","b"],"verifiers":[
		{"id":"a","command":["x"],"timeout_seconds":1},
		{"id":"b","command":["y"],"timeout_seconds":1},
		{"id":"c","command":["z"],"timeout_seconds":1},
		{"id":"d","command":["w"],"timeout_seconds":1}]}`))
	cur, _ := Parse([]byte(`{"defaults":["a"],"verifiers":[
		{"id":"a","command":["x"],"timeout_seconds":1},
		{"id":"b","command":["y"],"timeout_seconds":1},
		{"id":"c","command":["true"],"timeout_seconds":1},
		{"id":"e","command":["new"],"timeout_seconds":1}]}`))
	want := []string{"b no longer default", "c changed", "d removed"}
	if got := Weakened(base, cur); !reflect.DeepEqual(got, want) {
		t.Fatalf("Weakened = %q, want %q", got, want)
	}
	if got := Weakened(base, base); got != nil {
		t.Fatalf("identical registries weakened: %q", got)
	}
}
