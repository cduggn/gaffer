// Package beads is gaffer's boundary with the bd issue tracker. bd is the
// only store of task status and ownership; gaffer never mirrors it.
package beads

import (
	"errors"
	"strings"
)

// Issue is the subset of a bead gaffer reads.
type Issue struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Design      string   `json:"design"`
	Acceptance  string   `json:"acceptance_criteria"`
	Notes       string   `json:"notes"`
	Status      string   `json:"status"`
	Priority    int      `json:"priority"`
	Type        string   `json:"issue_type"`
	Assignee    string   `json:"assignee"`
	Labels      []string `json:"labels"`
	CloseReason string   `json:"close_reason"`
	Parent      string   `json:"parent"`
	Deps        []Dep    `json:"dependencies"`
}

// Dep is an edge from this issue to another.
type Dep struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	CloseReason string `json:"close_reason"`
	Type        string `json:"dependency_type"`
}

// Label returns the value of a "key:value" label, or "".
func (i Issue) Label(key string) string {
	for _, l := range i.Labels {
		if v, ok := strings.CutPrefix(l, key+":"); ok {
			return v
		}
	}
	return ""
}

func (i Issue) HasLabel(name string) bool {
	for _, l := range i.Labels {
		if l == name {
			return true
		}
	}
	return false
}

// ErrClaimed means another actor holds the issue.
var ErrClaimed = errors.New("issue is claimed by another actor")
