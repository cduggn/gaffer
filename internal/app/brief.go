package app

import (
	"fmt"
	"strings"

	"github.com/cduggn/gaffer/internal/config"
	"github.com/cduggn/gaffer/internal/verify"
	"github.com/cduggn/gaffer/internal/work"
)

// Mode says who finishes the run: the agent itself, or a dispatcher that
// verifies after the agent exits.
type Mode int

const (
	Interactive Mode = iota
	Dispatched
)

// Brief is the complete contract handed to a worker. A worker cannot ask
// questions, so everything it needs is here or pointed to from here.
func (a *App) Brief(run *work.Run, t Task, mode Mode) string {
	var b strings.Builder
	i := t.Issue
	fmt.Fprintf(&b, "# %s: %s\n\n", i.ID, i.Title)
	section(&b, "Goal", orDefault(i.Description, i.Title))
	if i.Design != "" {
		section(&b, "Design notes", i.Design)
	}
	if i.Notes != "" {
		section(&b, "Notes", i.Notes)
	}

	section(&b, "Workspace", fmt.Sprintf(
		"Work only in %s on branch %s (base %s). Do not touch other checkouts.",
		run.Worktree, run.Branch, short(run.BaseSHA)))

	var crit strings.Builder
	for _, c := range t.Criteria {
		fmt.Fprintf(&crit, "- %s: %s", c.ID, c.Outcome)
		if len(c.Verifiers) > 0 {
			fmt.Fprintf(&crit, " [verify: %s]", strings.Join(c.Verifiers, ", "))
		}
		if c.Review {
			crit.WriteString(" [review]")
		}
		crit.WriteString("\n")
	}
	section(&b, "Acceptance", crit.String())

	if reg, err := verify.Load(run.Worktree); err == nil {
		var v strings.Builder
		for _, id := range work.RequiredVerifiers(t.Criteria, reg.Defaults) {
			if ver, ok := reg.Get(id); ok {
				fmt.Fprintf(&v, "- %s: `%s` proves %q\n", id, strings.Join(ver.Command, " "), ver.Claim)
			} else {
				fmt.Fprintf(&v, "- %s: not registered yet. Add it to %s.\n", id, verify.RegistryPath)
			}
		}
		if v.Len() > 0 {
			section(&b, "Verifiers", v.String()+"\nYou may add verifiers. Removing or loosening one forces independent review.")
		}
	}

	if ctx := upstream(t); ctx != "" {
		section(&b, "Upstream context", ctx)
	}

	section(&b, "Playbook: "+t.Playbook.Name, strings.TrimSpace(stripHeading(t.Playbook.Steps())))

	finish := "When the work is complete:\n" +
		"1. Commit your changes in the worktree.\n" +
		"2. Run `gaffer verify`. Fix failures and rerun until it exits 0.\n"
	if t.Playbook.Review || work.NeedsReview(t.Criteria) {
		finish += "3. Run `gaffer handoff`, then have a reviewer on a different model run `gaffer review` using the brief it prints.\n" +
			"4. Run `gaffer done`.\n"
	} else {
		finish += "3. Run `gaffer done`.\n"
	}
	if mode == Dispatched {
		finish = "When the work is complete, commit it in the worktree, run `gaffer verify` until it exits 0, and exit. " +
			"The dispatcher re-verifies independently and handles review and closing. " +
			"If you cannot finish, run `gaffer block --reason \"<what a human must decide>\"` and exit.\n"
	}
	finish += "\nFile unrelated problems you find with `bd create \"<title>\" --deps discovered-from:" + i.ID + "` instead of fixing them here."
	section(&b, "Finish", finish)

	section(&b, "Never", "- weaken, skip, or delete a verifier or acceptance criterion to get a pass\n"+
		"- push, merge, rebase, or force anything outside this worktree's branch\n"+
		"- claim a check passed without running it")

	if s := config.Standing(a.Repo); s != "" {
		section(&b, "Standing orders", s)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// ReviewBrief is what an independent reviewer needs to judge a run.
func (a *App) ReviewBrief(run *work.Run, t Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Review %s: %s\n\n", t.Issue.ID, t.Issue.Title)
	section(&b, "Your role", "You are an independent reviewer. You did not write this change. "+
		"Judge it against the acceptance criteria, not against how you would have written it. "+
		"Run things. Do not trust the worker's summary.")
	section(&b, "Goal", orDefault(t.Issue.Description, t.Issue.Title))
	var crit strings.Builder
	for _, c := range t.Criteria {
		fmt.Fprintf(&crit, "- %s: %s\n", c.ID, c.Outcome)
	}
	section(&b, "Acceptance", crit.String())
	section(&b, "Change", fmt.Sprintf("cd %s\ngit diff %s...HEAD\ngit log --oneline %s..HEAD",
		run.Worktree, short(run.BaseSHA), short(run.BaseSHA)))
	if v, _ := a.Runs.LatestVerification(run.ID); v != nil {
		var s strings.Builder
		for _, c := range v.Checks {
			fmt.Fprintf(&s, "- %s: %s, log %s\n", c.ID, c.Status, c.Log)
		}
		if len(v.Weakened) > 0 {
			fmt.Fprintf(&s, "\nVerifiers changed since base: %s. Decide whether each change is justified.\n", strings.Join(v.Weakened, ", "))
		}
		section(&b, "Latest verification at "+v.Fingerprint.String(), s.String())
	}
	section(&b, "Verdict", fmt.Sprintf(
		"Do not edit files. Record exactly one verdict:\n"+
			"gaffer review %s --pass --notes \"<what you checked>\" --actor reviewer:<your model>\n"+
			"gaffer review %s --fail --notes \"<what must change>\" --actor reviewer:<your model>",
		t.Issue.ID, t.Issue.ID))
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// upstream relays what finished dependencies concluded, because a worker
// cannot see its siblings.
func upstream(t Task) string {
	var b strings.Builder
	for _, d := range t.Issue.Deps {
		if d.Status != "closed" {
			continue
		}
		fmt.Fprintf(&b, "- %s %s (%s): %s\n", d.ID, d.Title, d.Type, orDefault(d.CloseReason, "closed"))
	}
	return b.String()
}

func section(b *strings.Builder, title, body string) {
	fmt.Fprintf(b, "## %s\n\n%s\n\n", title, strings.TrimSpace(body))
}

func stripHeading(s string) string {
	if strings.HasPrefix(s, "# ") {
		if _, rest, ok := strings.Cut(s, "\n"); ok {
			return rest
		}
	}
	return s
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
