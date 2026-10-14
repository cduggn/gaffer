package work

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseAcceptance(t *testing.T) {
	got, err := ParseAcceptance(`Context prose is ignored.

- A1: retry writes one row per record [verify: export-retry, unit]
* [ ] A2) settings page explains the option [review]
- A3. both [verify: lint] [review]
`)
	if err != nil {
		t.Fatal(err)
	}
	want := []Criterion{
		{ID: "A1", Outcome: "retry writes one row per record", Verifiers: []string{"export-retry", "unit"}},
		{ID: "A2", Outcome: "settings page explains the option", Review: true},
		{ID: "A3", Outcome: "both", Verifiers: []string{"lint"}, Review: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestParseAcceptanceRejects(t *testing.T) {
	cases := map[string]string{
		"- A1: no proof":                     "A1 has no proof",
		"- A1: x [review]\n- A1: y [review]": "duplicated",
		"- A1: x [verify: ok, bad id]":       `bad verifier id "bad id"`,
	}
	for in, msg := range cases {
		_, err := ParseAcceptance(in)
		if err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("ParseAcceptance(%q) error = %v, want %q", in, err, msg)
		}
	}
}

func TestRunMove(t *testing.T) {
	r := &Run{ID: "r1", Phase: Blocked}
	now := time.Unix(0, 0)
	if err := r.Move(Done, "", now); err == nil || err.Error() != "run r1: cannot move blocked -> done" {
		t.Fatalf("blocked->done err = %v", err)
	}
	for _, p := range []Phase{Working, Review, Working, Review, Done} {
		if err := r.Move(p, "", now); err != nil {
			t.Fatal(err)
		}
	}
	if !r.Phase.Terminal() {
		t.Fatal("done should be terminal")
	}
	if err := r.Move(Working, "", now); err == nil {
		t.Fatal("done run must not reopen")
	}
}

func TestGate(t *testing.T) {
	tree := Fingerprint{Commit: "abc"}
	other := Fingerprint{Commit: "abc", Dirty: "0123456789abcdef"}
	verified := &Verification{Fingerprint: tree, Status: Pass, Checks: []CheckResult{{ID: "unit", Status: Pass}, {ID: "lint", Status: Pass}}}
	cmdOnly := []Criterion{{ID: "A1", Verifiers: []string{"unit"}}}
	reviewed := []Criterion{{ID: "A1", Verifiers: []string{"unit"}}, {ID: "A2", Review: true}}

	cases := []struct {
		name string
		e    Evidence
		want []string
	}{
		{"passes", Evidence{Criteria: cmdOnly, Defaults: []string{"lint"}, Current: tree, Verification: verified}, nil},
		{"no criteria", Evidence{Current: tree},
			[]string{"no acceptance criteria: add list items with [verify: <id>] or [review]"}},
		{"never verified", Evidence{Criteria: cmdOnly, Current: tree},
			[]string{"no verification has run: gaffer verify"}},
		{"stale", Evidence{Criteria: cmdOnly, Current: other, Verification: verified},
			[]string{"verification is stale: ran on abc, tree is now abc+0123456789ab", "uncommitted changes: commit them so the branch holds the work, then gaffer verify"}},
		{"default missing", Evidence{Criteria: cmdOnly, Defaults: []string{"e2e"}, Current: tree, Verification: verified},
			[]string{"required verifier e2e did not run"}},
		{"failed check", Evidence{Criteria: cmdOnly, Current: tree, Verification: &Verification{Fingerprint: tree, Checks: []CheckResult{{ID: "unit", Status: Fail}}}},
			[]string{"verifier unit: fail"}},
		{"check edited the tree", Evidence{Criteria: cmdOnly, Current: tree, Verification: &Verification{Fingerprint: tree, Status: BlockedStatus,
			Checks: []CheckResult{{ID: "unit", Status: Pass}, {ID: "tree-unchanged", Status: BlockedStatus, Reason: "checks modified the tree"}}}},
			[]string{"verifier tree-unchanged: blocked checks modified the tree"}},
		{"criteria changed after verification", Evidence{Criteria: cmdOnly, Task: "new", Current: tree, Verification: verified},
			[]string{"verification is stale: the acceptance criteria changed after it ran"}},
		{"criteria changed after review", Evidence{Criteria: reviewed, Worker: "w", Current: tree, Verification: verified, Verdict: &Verdict{Reviewer: "r", Pass: true, Fingerprint: tree, Task: "old"}},
			[]string{"review is stale: the acceptance criteria changed after it"}},
		{"uncommitted", Evidence{Criteria: cmdOnly, Current: other, Verification: &Verification{Fingerprint: other, Status: Pass, Checks: verified.Checks}},
			[]string{"uncommitted changes: commit them so the branch holds the work, then gaffer verify"}},
		{"wrong branch", Evidence{Criteria: cmdOnly, Current: tree, Verification: verified, Branch: "main", WantBranch: "gaffer/x"},
			[]string{"worktree is on main, not the run's branch gaffer/x"}},
		{"review missing", Evidence{Criteria: reviewed, Current: tree, Verification: verified},
			[]string{"review required: an independent reviewer must run gaffer review"}},
		{"self review", Evidence{Criteria: reviewed, Worker: "w", Current: tree, Verification: verified, Verdict: &Verdict{Reviewer: "w", Pass: true, Fingerprint: tree}},
			[]string{"review by w is not independent: the worker cannot review its own run"}},
		{"stale review", Evidence{Criteria: reviewed, Worker: "w", Current: tree, Verification: verified, Verdict: &Verdict{Reviewer: "r", Pass: true, Fingerprint: other}},
			[]string{"review is stale: reviewed abc+0123456789ab, tree is now abc"}},
		{"failed review", Evidence{Criteria: reviewed, Worker: "w", Current: tree, Verification: verified, Verdict: &Verdict{Reviewer: "r", Notes: "no tests", Fingerprint: tree}},
			[]string{"review failed: no tests"}},
		{"reviewed", Evidence{Criteria: reviewed, Worker: "w", Current: tree, Verification: verified, Verdict: &Verdict{Reviewer: "r", Pass: true, Fingerprint: tree}}, nil},
		{"weakened registry forces review", Evidence{Criteria: cmdOnly, Current: tree, Verification: &Verification{Fingerprint: tree, Status: Pass, Checks: verified.Checks, Weakened: []string{"unit"}}},
			[]string{"review required: verifiers changed since base (unit)"}},
		{"read-only changed", Evidence{Criteria: cmdOnly, ReadOnly: true, Changed: true, Current: tree, Verification: verified},
			[]string{"this playbook is read-only but the worktree differs from its base commit"}},
		{"handoff ignores a stale review", Evidence{Criteria: reviewed, SkipReview: true, Current: tree, Verification: verified, Verdict: &Verdict{Reviewer: "r", Fingerprint: other}}, nil},
		{"playbook demands review", Evidence{Criteria: cmdOnly, ReviewPolicy: true, Current: tree, Verification: verified},
			[]string{"review required: an independent reviewer must run gaffer review"}},
	}
	for _, c := range cases {
		if got := Gate(c.e); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Gate = %q, want %q", c.name, got, c.want)
		}
	}
}
