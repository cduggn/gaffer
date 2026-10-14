package playbook

import (
	"strings"
	"testing"

	"github.com/cduggn/gaffer/internal/beads"
)

func TestRoute(t *testing.T) {
	cases := []struct {
		issue beads.Issue
		want  string
	}{
		{beads.Issue{Type: "bug"}, "bugfix"},
		{beads.Issue{Type: "feature"}, "feature"},
		{beads.Issue{Type: "task"}, "chore"},
		{beads.Issue{Type: "decision"}, "chore"},
		{beads.Issue{Type: "feature", Labels: []string{"decompose"}}, "decompose"},
		{beads.Issue{Type: "bug", Labels: []string{"investigate"}}, "investigate"},
		{beads.Issue{Type: "task", Labels: []string{"refactor"}}, "refactor"},
		{beads.Issue{Type: "bug", Labels: []string{"playbook:feature"}}, "feature"},
	}
	for _, c := range cases {
		p, err := Route(c.issue)
		if err != nil || p.Name != c.want {
			t.Errorf("Route(%+v) = %q, %v; want %q", c.issue, p.Name, err, c.want)
		}
	}
	if _, err := Route(beads.Issue{Labels: []string{"playbook:nope"}}); err == nil || !strings.Contains(err.Error(), `unknown playbook "nope"`) {
		t.Errorf("unknown override err = %v", err)
	}
}

func TestEveryPlaybookIsEmbedded(t *testing.T) {
	for _, name := range Names() {
		p, _ := Get(name)
		if !strings.HasPrefix(p.Steps(), "# ") {
			t.Errorf("playbook %s has no heading", name)
		}
	}
}
