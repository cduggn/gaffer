// Package playbook routes a bead to the workflow that should handle it.
package playbook

import (
	"fmt"
	"strings"

	"github.com/cduggn/gaffer"
	"github.com/cduggn/gaffer/internal/beads"
)

// Playbook is a named workflow and the gate policy that comes with it.
type Playbook struct {
	Name string
	// Review means an independent reviewer must pass the run before it
	// closes, whatever its acceptance criteria say.
	Review bool
	// ReadOnly runs need no worktree; the deliverable is an answer or a plan.
	ReadOnly bool
	// Types and Labels select this playbook; first match in the table wins.
	Types  []string
	Labels []string
}

// table is ordered: label matches are checked before type matches so a label
// can specialize a type ("bug" + "investigate" is an investigation).
var table = []Playbook{
	{Name: "investigate", ReadOnly: true, Review: true, Labels: []string{"investigate", "question", "spike"}, Types: []string{"spike"}},
	{Name: "decompose", ReadOnly: true, Review: true, Labels: []string{"decompose"}},
	{Name: "refactor", Review: true, Labels: []string{"refactor"}},
	{Name: "bugfix", Types: []string{"bug"}},
	{Name: "feature", Review: true, Types: []string{"feature", "story"}},
	{Name: "chore", Types: []string{"chore", "task"}},
}

// Names lists every playbook.
func Names() []string {
	out := make([]string, len(table))
	for i, p := range table {
		out[i] = p.Name
	}
	return out
}

// Get returns a playbook by name.
func Get(name string) (Playbook, error) {
	for _, p := range table {
		if p.Name == name {
			return p, nil
		}
	}
	return Playbook{}, fmt.Errorf("unknown playbook %q (have %s)", name, strings.Join(Names(), ", "))
}

// Route picks the playbook for an issue. A "playbook:<name>" label overrides.
func Route(i beads.Issue) (Playbook, error) {
	if name := i.Label("playbook"); name != "" {
		return Get(name)
	}
	for _, p := range table {
		for _, l := range p.Labels {
			if i.HasLabel(l) {
				return p, nil
			}
		}
	}
	for _, p := range table {
		for _, t := range p.Types {
			if i.Type == t {
				return p, nil
			}
		}
	}
	return Get("chore")
}

// Steps is the playbook's prose, shipped inside the binary.
func (p Playbook) Steps() string {
	raw, err := gaffer.Playbooks.ReadFile("playbooks/" + p.Name + ".md")
	if err != nil {
		panic("playbook " + p.Name + " is not embedded: " + err.Error())
	}
	return string(raw)
}
