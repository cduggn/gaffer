package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cduggn/gaffer/internal/config"
	"github.com/cduggn/gaffer/internal/work"
)

// DispatchOptions bound an unattended batch.
type DispatchOptions struct {
	Agent     string        // worker agent name from config
	Reviewer  string        // reviewer agent name; empty leaves review to a human
	Max       int           // concurrent workers
	Limit     int           // total issues to start; 0 means until the queue drains
	Retries   int           // extra attempts after a failed verification
	Labels    []string      // only pick ready issues carrying all of these
	Heartbeat time.Duration // lease refresh cadence
	Log       io.Writer
}

// role is what a spawned process is doing for its run.
type role int

const (
	working role = iota
	reviewing
)

type exit struct {
	run  string
	role role
	err  error
}

// Dispatch claims ready issues, runs one agent per worktree, and verifies
// each run itself when its agent exits. It never trusts an agent's report:
// the gate decides. It returns when nothing is running and nothing more may
// start.
func (a *App) Dispatch(ctx context.Context, opt DispatchOptions) error {
	worker, err := config.LoadAgent(opt.Agent)
	if err != nil {
		return err
	}
	var reviewer config.Agent
	if opt.Reviewer != "" {
		if reviewer, err = config.LoadAgent(opt.Reviewer); err != nil {
			return err
		}
		if opt.Reviewer == opt.Agent {
			a.logf(opt, "warning: reviewer and worker are both %s; review is more useful from a different model", opt.Agent)
		}
	}
	if opt.Max < 1 {
		opt.Max = 1
	}
	if opt.Heartbeat <= 0 {
		opt.Heartbeat = time.Minute
	}

	exits := make(chan exit)
	running := map[string]*proc{}
	leases := map[string]context.CancelFunc{}
	started := 0
	drained := false

	// Each claimed run keeps its lease alive from claim until it settles,
	// including while the dispatcher verifies it.
	lease := func(run *work.Run) {
		if _, ok := leases[run.ID]; ok {
			return
		}
		lctx, cancel := context.WithCancel(ctx)
		leases[run.ID] = cancel
		go func(issue, actor string) {
			t := time.NewTicker(opt.Heartbeat)
			defer t.Stop()
			for {
				select {
				case <-lctx.Done():
					return
				case <-t.C:
					if err := a.Tracker.Heartbeat(issue, actor); err != nil {
						a.logf(opt, "%s: heartbeat: %v", issue, err)
					}
				}
			}
		}(run.Issue, run.Actor)
	}
	release := func(id string) {
		if cancel, ok := leases[id]; ok {
			cancel()
			delete(leases, id)
		}
	}
	defer func() {
		for id := range leases {
			release(id)
		}
	}()

	spawn := func(run *work.Run, r role, agent config.Agent, actor, brief string) error {
		pid, err := a.spawn(run, agent, actor, brief, r, exits)
		if err != nil {
			return err
		}
		if r == working {
			run.PID = pid
			run.Attempts++
			if err := a.Runs.Save(run); err != nil {
				return err
			}
		}
		running[run.ID] = &proc{role: r, pid: pid}
		return nil
	}

	for {
		for !drained && ctx.Err() == nil && len(running) < opt.Max && (opt.Limit == 0 || started < opt.Limit) {
			actor := "gaffer-" + opt.Agent + "-" + suffix()
			run, t, err := a.Next(opt.Labels, StartOptions{Actor: actor})
			if err != nil {
				a.logf(opt, "%v", err)
				drained = true
				break
			}
			started++
			lease(run)
			a.logf(opt, "%s: started run %s (%s) in %s", run.Issue, run.ID, t.Playbook.Name, run.Worktree)
			if err := spawn(run, working, worker, actor, a.Brief(run, t, Dispatched)); err != nil {
				release(run.ID)
				_ = a.Block(run, "could not start agent: "+err.Error())
				a.logf(opt, "%s: %v", run.Issue, err)
			}
		}
		if len(running) == 0 {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			a.interrupt(running, exits, opt)
			return ctx.Err()
		case ex := <-exits:
			delete(running, ex.run)
			run, err := a.Runs.Load(ex.run)
			if err != nil {
				a.logf(opt, "run %s: %v", ex.run, err)
				release(ex.run)
				continue
			}
			next, err := a.settle(ctx, run, ex, opt)
			if err != nil {
				a.logf(opt, "%s: %v", run.Issue, err)
				next = settled
			}
			switch next {
			case retry:
				t, err := a.Task(run.Issue)
				if err == nil {
					err = spawn(run, working, worker, run.Actor, a.Brief(run, t, Dispatched)+a.failureNote(run))
				}
				if err != nil {
					a.logf(opt, "%s: retry: %v", run.Issue, err)
					_ = a.Block(run, "could not restart agent: "+err.Error())
					release(run.ID)
				}
			case review:
				t, err := a.Task(run.Issue)
				if err == nil {
					err = spawn(run, reviewing, reviewer, "reviewer:"+opt.Reviewer, a.ReviewBrief(run, t))
				}
				if err != nil {
					a.logf(opt, "%s: review: %v", run.Issue, err)
					release(run.ID)
				}
			default:
				release(run.ID)
			}
			// A settled run may have unblocked dependents; look again.
			drained = false
		}
	}
}

type proc struct {
	role role
	pid  int
}

// interrupt stops every agent and returns its bead to the queue. Worktrees
// and evidence stay for inspection.
func (a *App) interrupt(running map[string]*proc, exits <-chan exit, opt DispatchOptions) {
	for _, p := range running {
		_ = syscall.Kill(-p.pid, syscall.SIGTERM)
	}
	deadline := time.After(10 * time.Second)
	for len(running) > 0 {
		select {
		case ex := <-exits:
			delete(running, ex.run)
			if run, err := a.Runs.Load(ex.run); err == nil && !run.Phase.Terminal() {
				if err := a.Abandon(run, "dispatch interrupted"); err != nil {
					a.logf(opt, "%s: %v", run.Issue, err)
				} else {
					a.logf(opt, "%s: interrupted; bead released, worktree kept at %s", run.Issue, run.Worktree)
				}
			}
		case <-deadline:
			for _, p := range running {
				_ = syscall.Kill(-p.pid, syscall.SIGKILL)
			}
			deadline = time.After(5 * time.Second)
		}
	}
}

type step int

const (
	settled step = iota
	retry
	review
)

// settle decides what happens after an agent exits, from evidence alone.
func (a *App) settle(ctx context.Context, run *work.Run, ex exit, opt DispatchOptions) (step, error) {
	if run.Phase.Terminal() || run.Phase == work.Blocked {
		a.logf(opt, "%s: agent left run %s", run.Issue, run.Phase)
		return settled, nil
	}
	if ex.role == reviewing {
		if err := a.Done(run); err != nil {
			a.logf(opt, "%s: after review: %v", run.Issue, err)
			if run.Phase == work.Working {
				return a.retryOrBlock(run, opt, "review failed")
			}
			return settled, a.Block(run, fmt.Sprintf("reviewer %s exited without a passing verdict on the current tree (exit: %v)", opt.Reviewer, ex.err))
		}
		a.logf(opt, "%s: done (reviewed)", run.Issue)
		return settled, nil
	}
	if ex.err != nil {
		a.logf(opt, "%s: agent exited: %v", run.Issue, ex.err)
	}
	v, err := a.Verify(ctx, run)
	if err != nil {
		return settled, err
	}
	if v.Status != work.Pass {
		a.logf(opt, "%s: verification %s", run.Issue, v.Status)
		return a.retryOrBlock(run, opt, "verification "+string(v.Status))
	}
	err = a.Done(run)
	if err == nil {
		a.logf(opt, "%s: done", run.Issue)
		return settled, nil
	}
	var gate *GateError
	if !errors.As(err, &gate) {
		return settled, err
	}
	if err := a.Handoff(run); err != nil {
		a.logf(opt, "%s: %v", run.Issue, err)
		return a.retryOrBlock(run, opt, "gate refused handoff")
	}
	if opt.Reviewer == "" {
		a.logf(opt, "%s: verified, waiting for review (gaffer review %s)", run.Issue, run.Issue)
		return settled, nil
	}
	a.logf(opt, "%s: verified, starting %s review", run.Issue, opt.Reviewer)
	return review, nil
}

func (a *App) retryOrBlock(run *work.Run, opt DispatchOptions, why string) (step, error) {
	if run.Attempts <= opt.Retries {
		if run.Phase == work.Review {
			if err := run.Move(work.Working, why, a.Now()); err != nil {
				return settled, err
			}
			if err := a.Runs.Save(run); err != nil {
				return settled, err
			}
		}
		a.logf(opt, "%s: %s, retrying (attempt %d)", run.Issue, why, run.Attempts+1)
		return retry, nil
	}
	return settled, a.Block(run, fmt.Sprintf("%s after %d attempt(s); evidence in %s", why, run.Attempts, a.Runs.RunDir(run.ID)))
}

// failureNote tells a retrying agent what went wrong last time.
func (a *App) failureNote(run *work.Run) string {
	var b strings.Builder
	b.WriteString("\n## Previous attempt\n\n")
	if v, _ := a.Runs.LatestVerification(run.ID); v != nil {
		fmt.Fprintf(&b, "Verification was %s:\n", v.Status)
		for _, c := range v.Checks {
			if c.Status == work.Pass {
				continue
			}
			fmt.Fprintf(&b, "- %s: %s %s\n", c.ID, c.Status, c.Reason)
			if tail := tailFile(c.Log, 40); tail != "" {
				fmt.Fprintf(&b, "```\n%s\n```\n", tail)
			}
		}
	}
	if r, _ := a.Runs.Verdict(run.ID); r != nil && !r.Pass {
		fmt.Fprintf(&b, "Review by %s failed: %s\n", r.Reviewer, r.Notes)
	}
	return b.String()
}

func (a *App) spawn(run *work.Run, agent config.Agent, actor, brief string, r role, exits chan<- exit) (int, error) {
	name := "agent"
	if r == reviewing {
		name = "review"
	}
	logPath := filepath.Join(a.Runs.RunDir(run.ID), fmt.Sprintf("%s-%d.log", name, run.Attempts+1))
	if err := os.WriteFile(strings.TrimSuffix(logPath, ".log")+".brief.md", []byte(brief), 0o644); err != nil {
		return 0, err
	}
	logf, err := os.Create(logPath)
	if err != nil {
		return 0, err
	}
	argv := agent.Expand(a.Repo, run.Worktree)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = run.Worktree
	cmd.Stdin = bytes.NewBufferString(brief)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(), "GAFFER_ACTOR="+actor, "GAFFER_RUN="+run.ID, "BEADS_ACTOR="+actor, "PATH="+selfPath())
	if err := cmd.Start(); err != nil {
		logf.Close()
		return 0, err
	}
	go func() {
		err := cmd.Wait()
		logf.Close()
		exits <- exit{run: run.ID, role: r, err: err}
	}()
	return cmd.Process.Pid, nil
}

// selfPath puts this gaffer binary first on PATH so agents call the same one.
func selfPath() string {
	path := os.Getenv("PATH")
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe) + string(os.PathListSeparator) + path
	}
	return path
}

func (a *App) logf(opt DispatchOptions, format string, args ...any) {
	if opt.Log != nil {
		fmt.Fprintf(opt.Log, "%s "+format+"\n", append([]any{a.Now().Format("15:04:05")}, args...)...)
	}
}

func suffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func tailFile(path string, lines int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	all := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}
