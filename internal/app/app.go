// Package app implements gaffer's commands over the tracker, git, the run
// store, and the verifier runner.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cduggn/gaffer/internal/beads"
	"github.com/cduggn/gaffer/internal/config"
	"github.com/cduggn/gaffer/internal/gitx"
	"github.com/cduggn/gaffer/internal/playbook"
	"github.com/cduggn/gaffer/internal/runs"
	"github.com/cduggn/gaffer/internal/verify"
	"github.com/cduggn/gaffer/internal/work"
)

// Tracker is the part of beads gaffer drives.
type Tracker interface {
	Ready(labels []string) ([]beads.Issue, error)
	Show(id string) (beads.Issue, error)
	Claim(id, actor string) error
	Release(id, actor string) error
	Heartbeat(id, actor string) error
	Comment(id, actor, text string) error
	SetStatus(id, actor, status string) error
	Close(id, actor, reason string) error
}

// App is one repository's view: its main checkout, tracker, and runs.
type App struct {
	Repo    string
	Tracker Tracker
	Runs    runs.Store
	Now     func() time.Time
}

// Open resolves the repository containing dir. Worktrees resolve to their
// main checkout so every run shares one tracker and one run store.
func Open(dir string) (*App, error) {
	root, err := gitx.MainRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("not inside a git repository: %w", err)
	}
	common, err := gitx.CommonDir(dir)
	if err != nil {
		return nil, err
	}
	return &App{
		Repo:    root,
		Tracker: beads.BD{Repo: root},
		Runs:    runs.Open(common),
		Now:     func() time.Time { return time.Now().UTC() },
	}, nil
}

// Actor names whoever is running gaffer. Reviewers pass --actor explicitly.
func Actor(flag string) string {
	for _, v := range []string{flag, os.Getenv("GAFFER_ACTOR"), os.Getenv("BEADS_ACTOR")} {
		if v != "" {
			return v
		}
	}
	if name, err := gitx.Git(".", "config", "user.name"); err == nil && name != "" {
		return name
	}
	return os.Getenv("USER")
}

// Task is an issue with its routing and criteria resolved.
type Task struct {
	Issue    beads.Issue
	Playbook playbook.Playbook
	Criteria []work.Criterion
}

func (a *App) Task(id string) (Task, error) {
	issue, err := a.Tracker.Show(id)
	if err != nil {
		return Task{}, err
	}
	pb, err := playbook.Route(issue)
	if err != nil {
		return Task{}, err
	}
	criteria, err := work.ParseAcceptance(issue.Acceptance)
	if err != nil {
		return Task{}, fmt.Errorf("%s: %w", id, err)
	}
	return Task{Issue: issue, Playbook: pb, Criteria: criteria}, nil
}

// Digest identifies the acceptance criteria evidence was gathered against.
func (t Task) Digest() string {
	sum := sha256.Sum256([]byte(t.Issue.Acceptance))
	return hex.EncodeToString(sum[:8])
}

// Lint reports every reason a bead is not ready to be worked.
func (a *App) Lint(id string) ([]string, error) {
	issue, err := a.Tracker.Show(id)
	if err != nil {
		return nil, err
	}
	var problems []string
	if _, err := playbook.Route(issue); err != nil {
		problems = append(problems, err.Error())
	}
	criteria, err := work.ParseAcceptance(issue.Acceptance)
	if err != nil {
		problems = append(problems, err.Error())
	} else if len(criteria) == 0 {
		problems = append(problems, "no acceptance criteria: add list items like `- A1: <outcome> [verify: <id>]` or `[review]`")
	}
	reg, err := verify.Load(a.Repo)
	if err != nil {
		return append(problems, err.Error()), nil
	}
	for _, id := range work.RequiredVerifiers(criteria, nil) {
		if _, ok := reg.Get(id); !ok {
			problems = append(problems, "verifier "+id+" is not registered in "+verify.RegistryPath)
		}
	}
	return problems, nil
}

// StartOptions shape how a run begins.
type StartOptions struct {
	Actor string
	Base  string // commit or branch the worktree starts from; default HEAD
}

// Start claims an issue and prepares its worktree. Starting an issue you
// already run returns that run, so a crashed start can simply be retried.
func (a *App) Start(id string, opt StartOptions) (*work.Run, Task, error) {
	t, err := a.Task(id)
	if err != nil {
		return nil, Task{}, err
	}
	if problems, err := a.Lint(id); err != nil {
		return nil, t, err
	} else if len(problems) > 0 {
		return nil, t, fmt.Errorf("%s is not ready to work:\n  %s", id, strings.Join(problems, "\n  "))
	}
	if existing, err := a.Runs.Active(id); err != nil {
		return nil, t, err
	} else if existing != nil {
		if existing.Actor != opt.Actor {
			return nil, t, fmt.Errorf("%s: %w (run %s by %s)", id, beads.ErrClaimed, existing.ID, existing.Actor)
		}
		return existing, t, gitx.AddWorktree(a.Repo, existing.Worktree, existing.Branch, existing.BaseSHA)
	}
	if err := a.Tracker.Claim(id, opt.Actor); err != nil {
		return nil, t, err
	}
	base := opt.Base
	if base == "" {
		base = "HEAD"
	}
	sha, err := gitx.Git(a.Repo, "rev-parse", base+"^{commit}")
	if err != nil {
		return nil, t, err
	}
	now := a.Now()
	run := &work.Run{
		ID:       runs.NewID(now),
		Issue:    id,
		Playbook: t.Playbook.Name,
		Actor:    opt.Actor,
		Phase:    work.Working,
		Repo:     a.Repo,
		Worktree: filepath.Join(config.WorktreeRoot(), repoKey(a.Repo), id),
		Branch:   "gaffer/" + id,
		BaseSHA:  sha,
		Started:  now,
		Updated:  now,
	}
	if err := gitx.AddWorktree(a.Repo, run.Worktree, run.Branch, sha); err != nil {
		_ = a.Tracker.Release(id, opt.Actor)
		return nil, t, err
	}
	if err := a.Runs.Save(run); err != nil {
		return nil, t, err
	}
	return run, t, nil
}

// repoKey names a repository's worktree directory. The hash keeps two clones
// with the same basename from sharing worktrees.
func repoKey(repo string) string {
	sum := sha256.Sum256([]byte(repo))
	return filepath.Base(repo) + "-" + hex.EncodeToString(sum[:3])
}

// Next starts the highest-priority ready issue this actor can claim.
func (a *App) Next(labels []string, opt StartOptions) (*work.Run, Task, error) {
	ready, err := a.Tracker.Ready(labels)
	if err != nil {
		return nil, Task{}, err
	}
	var skipped []string
	for _, issue := range ready {
		run, t, err := a.Start(issue.ID, opt)
		if err == nil {
			return run, t, nil
		}
		if errors.Is(err, beads.ErrClaimed) {
			continue
		}
		skipped = append(skipped, err.Error())
	}
	msg := "no ready work"
	if len(skipped) > 0 {
		msg += "; skipped:\n  " + strings.Join(skipped, "\n  ")
	}
	return nil, Task{}, errors.New(msg)
}

// Resolve finds the run an id refers to: an issue id, a run id, or empty for
// the run whose worktree contains the current directory.
func (a *App) Resolve(id string) (*work.Run, error) {
	if id == "" {
		cwd, _ := os.Getwd()
		top, err := gitx.Root(cwd)
		if err != nil {
			return nil, err
		}
		run, err := a.Runs.ByWorktree(top)
		if err != nil || run != nil {
			return run, err
		}
		return nil, errors.New("no active run for this directory: pass an issue id")
	}
	if run, err := a.Runs.Active(id); err != nil || run != nil {
		return run, err
	}
	if run, err := a.Runs.Load(id); err == nil {
		return run, nil
	}
	return nil, fmt.Errorf("no active run for %s: start one with gaffer start %s", id, id)
}

// Verify runs every required verifier in the run's worktree and records the
// result, using the registry as committed in the worktree.
func (a *App) Verify(ctx context.Context, run *work.Run) (*work.Verification, error) {
	t, err := a.Task(run.Issue)
	if err != nil {
		return nil, err
	}
	reg, err := verify.Load(run.Worktree)
	if err != nil {
		return nil, err
	}
	ids := work.RequiredVerifiers(t.Criteria, reg.Defaults)
	now := a.Now()
	vid := now.Format("20060102T150405.000000000")
	logDir := filepath.Join(a.Runs.RunDir(run.ID), "verifications", vid)
	v, err := verify.Run(ctx, reg, run.Worktree, logDir, ids, gitx.Fingerprint)
	if err != nil {
		return nil, err
	}
	v.ID, v.Run, v.Issue, v.Task = vid, run.ID, run.Issue, t.Digest()
	if base, err := baseRegistry(run); err != nil {
		return nil, err
	} else {
		v.Weakened = verify.Weakened(base, reg)
	}
	if err := a.Runs.SaveVerification(&v); err != nil {
		return nil, err
	}
	return &v, nil
}

func baseRegistry(run *work.Run) (*verify.Registry, error) {
	raw, err := gitx.Git(run.Worktree, "show", run.BaseSHA+":"+verify.RegistryPath)
	if err != nil {
		return verify.Parse([]byte(`{"verifiers":[]}`))
	}
	return verify.Parse([]byte(raw))
}

// Evidence gathers what the gate needs for a run's current tree.
func (a *App) Evidence(run *work.Run) (work.Evidence, error) {
	t, err := a.Task(run.Issue)
	if err != nil {
		return work.Evidence{}, err
	}
	reg, err := verify.Load(run.Worktree)
	if err != nil {
		return work.Evidence{}, err
	}
	current, err := gitx.Fingerprint(run.Worktree)
	if err != nil {
		return work.Evidence{}, err
	}
	v, err := a.Runs.LatestVerification(run.ID)
	if err != nil {
		return work.Evidence{}, err
	}
	verdict, err := a.Runs.Verdict(run.ID)
	if err != nil {
		return work.Evidence{}, err
	}
	// The run's playbook was fixed at start; relabelling the bead later must
	// not relax the gate.
	pb, err := playbook.Get(run.Playbook)
	if err != nil {
		return work.Evidence{}, err
	}
	branch, err := gitx.Git(run.Worktree, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return work.Evidence{}, err
	}
	return work.Evidence{
		Criteria:     t.Criteria,
		Defaults:     reg.Defaults,
		Worker:       run.Actor,
		Current:      current,
		Verification: v,
		Verdict:      verdict,
		ReviewPolicy: pb.Review,
		ReadOnly:     pb.ReadOnly,
		Changed:      current.Dirty != "" || current.Commit != run.BaseSHA,
		Task:         t.Digest(),
		Branch:       branch,
		WantBranch:   run.Branch,
	}, nil
}

// Handoff moves a verified run to review. Everything except the review
// itself must already pass.
func (a *App) Handoff(run *work.Run) error {
	e, err := a.Evidence(run)
	if err != nil {
		return err
	}
	e.SkipReview = true
	if reasons := work.Gate(e); len(reasons) > 0 {
		return &GateError{Reasons: reasons}
	}
	if err := run.Move(work.Review, "awaiting independent review", a.Now()); err != nil {
		return err
	}
	return a.Runs.Save(run)
}

// Review records an independent verdict on the run's current tree.
func (a *App) Review(run *work.Run, reviewer string, pass bool, notes string) error {
	if reviewer == run.Actor {
		return fmt.Errorf("%s cannot review its own run: pass --actor reviewer:<name>", reviewer)
	}
	// A dispatched worker runs with GAFFER_ACTOR set to its own identity, so
	// renaming itself with --actor does not make it a different reviewer.
	if env := os.Getenv("GAFFER_ACTOR"); env == run.Actor {
		return fmt.Errorf("this process is %s, the worker on %s: review must come from another session", env, run.Issue)
	}
	if !pass && strings.TrimSpace(notes) == "" {
		return errors.New("a failing review needs --notes saying what to fix")
	}
	fp, err := gitx.Fingerprint(run.Worktree)
	if err != nil {
		return err
	}
	t, err := a.Task(run.Issue)
	if err != nil {
		return err
	}
	verdict := &work.Verdict{Reviewer: reviewer, Pass: pass, Fingerprint: fp, Notes: notes, Task: t.Digest(), At: a.Now()}
	if err := a.Runs.SaveVerdict(run.ID, verdict); err != nil {
		return err
	}
	word := "passed"
	if !pass {
		word = "failed"
		if run.Phase == work.Review {
			if err := run.Move(work.Working, "review failed: "+notes, a.Now()); err != nil {
				return err
			}
			if err := a.Runs.Save(run); err != nil {
				return err
			}
		}
	}
	return a.Tracker.Comment(run.Issue, reviewer, fmt.Sprintf("gaffer review %s at %s: %s", word, fp, notes))
}

// GateError lists every reason a run may not close.
type GateError struct{ Reasons []string }

func (e *GateError) Error() string {
	return "not done:\n  " + strings.Join(e.Reasons, "\n  ")
}

// Done closes the issue if and only if the gate passes.
func (a *App) Done(run *work.Run) error {
	if !run.Phase.CanMove(work.Done) {
		return fmt.Errorf("run %s is %s: gaffer resume first", run.ID, run.Phase)
	}
	e, err := a.Evidence(run)
	if err != nil {
		return err
	}
	if reasons := work.Gate(e); len(reasons) > 0 {
		return &GateError{Reasons: reasons}
	}
	summary := closeSummary(run, e)
	if err := a.Tracker.Comment(run.Issue, run.Actor, summary); err != nil {
		return err
	}
	if err := a.Tracker.Close(run.Issue, run.Actor, firstLine(summary)); err != nil {
		return err
	}
	if err := run.Move(work.Done, "closed", a.Now()); err != nil {
		return err
	}
	return a.Runs.Save(run)
}

func closeSummary(run *work.Run, e work.Evidence) string {
	var b strings.Builder
	fmt.Fprintf(&b, "gaffer: done on %s at %s\n", run.Branch, e.Current)
	if v := e.Verification; v != nil {
		for _, c := range v.Checks {
			fmt.Fprintf(&b, "- verifier %s: %s (%.1fs)\n", c.ID, c.Status, c.Seconds)
		}
	}
	if r := e.Verdict; r != nil {
		fmt.Fprintf(&b, "- reviewed by %s: pass. %s\n", r.Reviewer, r.Notes)
	}
	for _, c := range e.Criteria {
		fmt.Fprintf(&b, "- %s: %s\n", c.ID, c.Outcome)
	}
	return strings.TrimRight(b.String(), "\n")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// Block parks a run on something only a human can resolve.
func (a *App) Block(run *work.Run, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("say what is needed with --reason")
	}
	if err := run.Move(work.Blocked, reason, a.Now()); err != nil {
		return err
	}
	if err := a.Runs.Save(run); err != nil {
		return err
	}
	if err := a.Tracker.SetStatus(run.Issue, run.Actor, "blocked"); err != nil {
		return err
	}
	return a.Tracker.Comment(run.Issue, run.Actor, "gaffer: blocked: "+reason)
}

// Resume returns a blocked or reviewed run to work.
func (a *App) Resume(run *work.Run) error {
	if run.Phase == work.Blocked {
		if err := a.Tracker.SetStatus(run.Issue, run.Actor, "in_progress"); err != nil {
			return err
		}
	}
	if err := run.Move(work.Working, "resumed", a.Now()); err != nil {
		return err
	}
	return a.Runs.Save(run)
}

// Abandon ends a run without closing its issue and releases the claim. The
// worktree and branch stay for inspection.
func (a *App) Abandon(run *work.Run, reason string) error {
	if err := run.Move(work.Abandoned, reason, a.Now()); err != nil {
		return err
	}
	if err := a.Runs.Save(run); err != nil {
		return err
	}
	if err := a.Tracker.Release(run.Issue, run.Actor); err != nil {
		return err
	}
	if reason != "" {
		return a.Tracker.Comment(run.Issue, run.Actor, "gaffer: abandoned run "+run.ID+": "+reason)
	}
	return nil
}
