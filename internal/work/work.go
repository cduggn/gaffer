// Package work holds gaffer's domain model: the run lifecycle, acceptance
// criteria parsed from a bead, and the evidence that gates completion.
package work

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Phase is where a run sits in its lifecycle. Legal moves live in one table.
type Phase string

const (
	Working   Phase = "working"
	Review    Phase = "review"
	Blocked   Phase = "blocked"
	Done      Phase = "done"
	Abandoned Phase = "abandoned"
)

// Verification is an event recorded against a run, not a phase: a run can be
// verified many times while it is working.
var transitions = map[Phase][]Phase{
	Working: {Review, Blocked, Done, Abandoned},
	Review:  {Working, Blocked, Done, Abandoned},
	Blocked: {Working, Abandoned},
}

func (p Phase) Terminal() bool { return p == Done || p == Abandoned }

// CanMove reports whether a run may go from p to next.
func (p Phase) CanMove(next Phase) bool {
	for _, allowed := range transitions[p] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Run is one attempt at one bead, owned by one actor in one worktree.
type Run struct {
	ID       string    `json:"id"`
	Issue    string    `json:"issue"`
	Playbook string    `json:"playbook"`
	Actor    string    `json:"actor"`
	Phase    Phase     `json:"phase"`
	Repo     string    `json:"repo"`
	Worktree string    `json:"worktree"`
	Branch   string    `json:"branch"`
	BaseSHA  string    `json:"base_sha"`
	PID      int       `json:"pid,omitempty"`
	Attempts int       `json:"attempts"`
	Note     string    `json:"note,omitempty"`
	Started  time.Time `json:"started"`
	Updated  time.Time `json:"updated"`
}

// Move advances the run, refusing transitions the lifecycle does not allow.
func (r *Run) Move(next Phase, note string, now time.Time) error {
	if r.Phase == next {
		r.Note, r.Updated = note, now
		return nil
	}
	if !r.Phase.CanMove(next) {
		return fmt.Errorf("run %s: cannot move %s -> %s", r.ID, r.Phase, next)
	}
	r.Phase, r.Note, r.Updated = next, note, now
	return nil
}

// Criterion is one observable outcome and the proof it requires.
type Criterion struct {
	ID        string   `json:"id"`
	Outcome   string   `json:"outcome"`
	Verifiers []string `json:"verifiers,omitempty"`
	Review    bool     `json:"review,omitempty"`
}

var (
	criterionLine = regexp.MustCompile(`^\s*[-*]\s*(?:\[[ xX]\]\s*)?([A-Za-z][A-Za-z0-9_-]*)\s*[:.)]\s*(.+?)\s*$`)
	proofTag      = regexp.MustCompile(`\[(verify|review)(?::\s*([^\]]*))?\]`)
	identifier    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

// ParseAcceptance reads criteria written as list items in a bead's
// acceptance field:
//
//   - A1: retrying an export writes one row per record [verify: export-retry]
//   - A2: the settings page explains the new option [review]
//
// Lines that are not list items are prose and are ignored. A criterion with
// no proof tag is an error: unverifiable acceptance is what this tool exists
// to reject.
func ParseAcceptance(text string) ([]Criterion, error) {
	var out []Criterion
	seen := map[string]bool{}
	for n, line := range strings.Split(text, "\n") {
		m := criterionLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		c := Criterion{ID: m[1]}
		body := m[2]
		for _, tag := range proofTag.FindAllStringSubmatch(body, -1) {
			if tag[1] == "review" {
				c.Review = true
				continue
			}
			for _, id := range strings.Split(tag[2], ",") {
				id = strings.TrimSpace(id)
				if !identifier.MatchString(id) {
					return nil, fmt.Errorf("acceptance line %d: bad verifier id %q", n+1, id)
				}
				c.Verifiers = append(c.Verifiers, id)
			}
		}
		c.Outcome = strings.TrimSpace(proofTag.ReplaceAllString(body, ""))
		if len(c.Verifiers) == 0 && !c.Review {
			return nil, fmt.Errorf("acceptance %s has no proof: add [verify: <id>] or [review]", c.ID)
		}
		if seen[c.ID] {
			return nil, fmt.Errorf("acceptance id %s is duplicated", c.ID)
		}
		seen[c.ID] = true
		out = append(out, c)
	}
	return out, nil
}

// RequiredVerifiers is the ordered, de-duplicated set of verifier ids the
// criteria and the project defaults demand.
func RequiredVerifiers(criteria []Criterion, defaults []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range defaults {
		add(id)
	}
	for _, c := range criteria {
		for _, id := range c.Verifiers {
			add(id)
		}
	}
	return out
}

// NeedsReview reports whether any criterion can only be proven by review.
func NeedsReview(criteria []Criterion) bool {
	for _, c := range criteria {
		if c.Review {
			return true
		}
	}
	return false
}
