package work

import (
	"fmt"
	"strings"
	"time"
)

// Fingerprint identifies the exact tree a check ran against: the commit plus
// a digest of uncommitted and untracked changes.
type Fingerprint struct {
	Commit string `json:"commit"`
	Dirty  string `json:"dirty"`
}

func (f Fingerprint) String() string {
	if f.Dirty == "" {
		return f.Commit
	}
	return f.Commit + "+" + f.Dirty[:12]
}

type Status string

const (
	Pass          Status = "pass"
	Fail          Status = "fail"
	BlockedStatus Status = "blocked"
)

// CheckResult is one verifier's outcome in one verification.
type CheckResult struct {
	ID       string   `json:"id"`
	Claim    string   `json:"claim"`
	Command  []string `json:"command"`
	Status   Status   `json:"status"`
	ExitCode *int     `json:"exit_code,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Log      string   `json:"log"`
	Seconds  float64  `json:"seconds"`
}

// Verification is a single fresh execution of every required verifier.
type Verification struct {
	ID          string        `json:"id"`
	Run         string        `json:"run"`
	Issue       string        `json:"issue"`
	Fingerprint Fingerprint   `json:"fingerprint"`
	Started     time.Time     `json:"started"`
	Finished    time.Time     `json:"finished"`
	Status      Status        `json:"status"`
	Checks      []CheckResult `json:"checks"`
	// Task digests the acceptance criteria the checks were selected from.
	Task string `json:"task"`
	// Weakened lists verifiers that existed at the run's base commit and were
	// removed, demoted from defaults, or changed. Any entry forces review.
	Weakened []string `json:"weakened,omitempty"`
}

// Verdict is an independent reviewer's judgement of one tree.
type Verdict struct {
	Reviewer    string      `json:"reviewer"`
	Pass        bool        `json:"pass"`
	Fingerprint Fingerprint `json:"fingerprint"`
	Notes       string      `json:"notes"`
	Task        string      `json:"task"`
	At          time.Time   `json:"at"`
}

// Evidence is everything the completion gate looks at.
type Evidence struct {
	Criteria     []Criterion
	Defaults     []string
	Worker       string
	Current      Fingerprint
	Verification *Verification
	Verdict      *Verdict
	ReviewPolicy bool // the playbook demands review regardless of criteria
	ReadOnly     bool // the playbook forbids changing files
	Changed      bool // the tree differs from the run's base commit
	// SkipReview evaluates everything except review, for handing off.
	SkipReview bool
	// Task digests the current acceptance criteria; evidence gathered
	// against different criteria is stale.
	Task string
	// Branch is the branch the worktree is on; WantBranch is the run's.
	Branch, WantBranch string
}

// Gate returns every reason the work may not close. No reasons means done.
func Gate(e Evidence) []string {
	var reasons []string
	if len(e.Criteria) == 0 {
		reasons = append(reasons, "no acceptance criteria: add list items with [verify: <id>] or [review]")
	}
	required := RequiredVerifiers(e.Criteria, e.Defaults)
	if len(required) > 0 {
		v := e.Verification
		switch {
		case v == nil:
			reasons = append(reasons, "no verification has run: gaffer verify")
		case v.Fingerprint != e.Current:
			reasons = append(reasons, fmt.Sprintf("verification is stale: ran on %s, tree is now %s", v.Fingerprint, e.Current))
		case v.Task != e.Task:
			reasons = append(reasons, "verification is stale: the acceptance criteria changed after it ran")
		default:
			ran := map[string]bool{}
			before := len(reasons)
			for _, c := range v.Checks {
				ran[c.ID] = true
				if c.Status != Pass {
					reasons = append(reasons, strings.TrimSpace(fmt.Sprintf("verifier %s: %s %s", c.ID, c.Status, c.Reason)))
				}
			}
			for _, id := range required {
				if !ran[id] {
					reasons = append(reasons, "required verifier "+id+" did not run")
				}
			}
			if len(reasons) == before && v.Status != Pass {
				reasons = append(reasons, "verification was "+string(v.Status))
			}
		}
	}
	if e.WantBranch != "" && e.Branch != e.WantBranch {
		reasons = append(reasons, fmt.Sprintf("worktree is on %s, not the run's branch %s", e.Branch, e.WantBranch))
	}
	if !e.ReadOnly && e.Current.Dirty != "" {
		reasons = append(reasons, "uncommitted changes: commit them so the branch holds the work, then gaffer verify")
	}
	if e.ReadOnly && e.Changed {
		reasons = append(reasons, "this playbook is read-only but the worktree differs from its base commit")
	}
	weakened := e.Verification != nil && len(e.Verification.Weakened) > 0
	if !e.SkipReview && (e.ReviewPolicy || NeedsReview(e.Criteria) || weakened) {
		r := e.Verdict
		switch {
		case r == nil && weakened:
			reasons = append(reasons, "review required: verifiers changed since base ("+strings.Join(e.Verification.Weakened, ", ")+")")
		case r == nil:
			reasons = append(reasons, "review required: an independent reviewer must run gaffer review")
		case r.Reviewer == e.Worker:
			reasons = append(reasons, "review by "+r.Reviewer+" is not independent: the worker cannot review its own run")
		case r.Fingerprint != e.Current:
			reasons = append(reasons, fmt.Sprintf("review is stale: reviewed %s, tree is now %s", r.Fingerprint, e.Current))
		case r.Task != e.Task:
			reasons = append(reasons, "review is stale: the acceptance criteria changed after it")
		case !r.Pass:
			reasons = append(reasons, "review failed: "+r.Notes)
		}
	}
	return reasons
}
