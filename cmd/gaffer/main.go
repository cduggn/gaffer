// Command gaffer runs beads tasks through playbooks, verifiers, and a
// completion gate. See README.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/cduggn/gaffer"
	"github.com/cduggn/gaffer/internal/app"
	"github.com/cduggn/gaffer/internal/config"
	"github.com/cduggn/gaffer/internal/playbook"
	"github.com/cduggn/gaffer/internal/verify"
	"github.com/cduggn/gaffer/internal/work"
)

var version = "dev"

// Exit codes. Blocked is distinct from failed so scripts can tell "fix the
// code" from "a human or the environment must act".
const (
	exitOK      = 0
	exitFail    = 1
	exitBlocked = 2
	exitUsage   = 64
)

type command struct {
	usage string
	run   func(args []string) error
}

// Commands are registered in init because their handlers print usage lines
// from this table.
var commands map[string]command

func init() {
	commands = map[string]command{
		"init":          {"init [--tracked]              scaffold .gaffer/ and beads in this repo", cmdInit},
		"status":        {"status [--json]              active runs and the ready queue", cmdStatus},
		"next":          {"next [--label L] [--actor A] claim the top ready bead and print its brief", cmdNext},
		"start":         {"start <id> [--actor A]       claim a specific bead and print its brief", cmdStart},
		"brief":         {"brief [<id>] [--review]      reprint a run's brief or review brief", cmdBrief},
		"lint":          {"lint <id>...                 check beads are ready to work", cmdLint},
		"verify":        {"verify [<id>] [--json]       run required verifiers on the current tree", cmdVerify},
		"handoff":       {"handoff [<id>]               move a verified run to review", cmdHandoff},
		"review":        {"review <id> --pass|--fail --notes N --actor A   record an independent verdict", cmdReview},
		"done":          {"done [<id>]                  close the bead if the gate passes", cmdDone},
		"block":         {"block [<id>] --reason R      park a run on a human decision", cmdBlock},
		"resume":        {"resume [<id>]                return a blocked or reviewed run to work", cmdResume},
		"abandon":       {"abandon [<id>] [--reason R]  end a run and release the bead", cmdAbandon},
		"dispatch":      {"dispatch [--agent claude] [--reviewer codex] [--max N] [--limit N]   run ready beads unattended", cmdDispatch},
		"lesson":        {"lesson \"<rule>\" [--global]   add a standing order to every future brief", cmdLesson},
		"playbooks":     {"playbooks [<name>]           list playbooks or print one", cmdPlaybooks},
		"install-skill": {"install-skill [--dir D]      install the agent skill (default ~/.claude/skills)", cmdInstallSkill},
	}
}

var order = []string{"init", "status", "next", "start", "brief", "lint", "verify", "handoff", "review", "done", "block", "resume", "abandon", "dispatch", "lesson", "playbooks", "install-skill"}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		usage(os.Stdout)
		return
	}
	if os.Args[1] == "version" || os.Args[1] == "--version" {
		fmt.Println("gaffer", version)
		return
	}
	c, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "gaffer: unknown command %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(exitUsage)
	}
	err := c.run(os.Args[2:])
	os.Exit(report(err))
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "gaffer: run beads tasks through playbooks, verifiers, and a completion gate")
	fmt.Fprintln(w)
	for _, name := range order {
		fmt.Fprintln(w, "  gaffer "+commands[name].usage)
	}
}

type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func report(err error) int {
	if err == nil {
		return exitOK
	}
	var ex *exitError
	if errors.As(err, &ex) {
		if ex.msg != "" {
			fmt.Fprintln(os.Stderr, ex.msg)
		}
		return ex.code
	}
	fmt.Fprintln(os.Stderr, "gaffer:", err)
	var gate *app.GateError
	if errors.As(err, &gate) || errors.Is(err, flag.ErrHelp) {
		return exitFail
	}
	return exitFail
}

// parse handles flags appearing before or after positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, &exitError{code: exitUsage}
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func open() (*app.App, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return app.Open(cwd)
}

func one(pos []string) string {
	if len(pos) > 0 {
		return pos[0]
	}
	return ""
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	tracked := fs.Bool("tracked", false, "let beads commit its setup to this repo (default keeps beads local and leaves git untouched)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	a, err := open()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(a.Repo, ".beads")); errors.Is(err, os.ErrNotExist) {
		// Plain bd init commits, edits agent instruction files, and sets
		// core.hooksPath. gaffer brings its own skill, so skip all of that.
		bdArgs := []string{"init", "--quiet", "--skip-agents", "--skip-hooks", "--non-interactive"}
		if !*tracked {
			bdArgs = append(bdArgs, "--stealth")
		}
		if err := runBd(a.Repo, bdArgs...); err != nil {
			return fmt.Errorf("bd init: %w", err)
		}
		fmt.Println("initialized beads")
	}
	reg := filepath.Join(a.Repo, verify.RegistryPath)
	if _, err := os.Stat(reg); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(reg), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(reg, []byte(starterRegistry(a.Repo)), 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", verify.RegistryPath, "(edit the commands to match this project)")
	}
	standing := filepath.Join(a.Repo, config.StandingPath)
	if _, err := os.Stat(standing); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(standing, []byte("- Keep changes inside the task's scope; file anything else as a new bead.\n"), 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", config.StandingPath)
	}
	if _, err := verify.Load(a.Repo); err != nil {
		return err
	}
	fmt.Println("ready: commit .gaffer/, then create beads with acceptance criteria and run gaffer next")
	return nil
}

// starterRegistry guesses one default check from the project's build files.
func starterRegistry(repo string) string {
	cmd, claim := []string{"true"}, "placeholder: replace with this project's test command"
	switch {
	case exists(repo, "go.mod"):
		cmd, claim = []string{"go", "test", "./..."}, "Go tests pass"
	case exists(repo, "package.json"):
		cmd, claim = []string{"npm", "test", "--silent"}, "npm tests pass"
	case exists(repo, "Cargo.toml"):
		cmd, claim = []string{"cargo", "test", "--quiet"}, "Rust tests pass"
	case exists(repo, "pyproject.toml"), exists(repo, "setup.py"):
		cmd, claim = []string{"python3", "-m", "pytest", "-q"}, "Python tests pass"
	case exists(repo, "Makefile"):
		cmd, claim = []string{"make", "test"}, "make test passes"
	}
	reg := verify.Registry{
		Defaults:  []string{"tests"},
		Verifiers: []verify.Verifier{{ID: "tests", Scope: "project", Claim: claim, Command: cmd, Timeout: 600}},
	}
	raw, _ := json.MarshalIndent(reg, "", "  ")
	return string(raw) + "\n"
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func runBd(dir string, args ...string) error {
	cmd := execCommand("bd", args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

type statusView struct {
	Runs  []*work.Run `json:"runs"`
	Ready []readyItem `json:"ready"`
}

type readyItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Priority int    `json:"priority"`
	Playbook string `json:"playbook"`
	Problem  string `json:"problem,omitempty"`
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "machine-readable output")
	all := fs.Bool("all", false, "include finished runs")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	a, err := open()
	if err != nil {
		return err
	}
	runsAll, err := a.Runs.All()
	if err != nil {
		return err
	}
	var view statusView
	for _, r := range runsAll {
		if *all || !r.Phase.Terminal() {
			view.Runs = append(view.Runs, r)
		}
	}
	ready, err := a.Tracker.Ready(nil)
	if err != nil {
		return err
	}
	for _, i := range ready {
		item := readyItem{ID: i.ID, Title: i.Title, Priority: i.Priority}
		if pb, err := playbook.Route(i); err == nil {
			item.Playbook = pb.Name
		}
		if problems, err := a.Lint(i.ID); err == nil && len(problems) > 0 {
			item.Problem = problems[0]
		}
		view.Ready = append(view.Ready, item)
	}
	if *asJSON {
		return printJSON(view)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "RUNS\tPHASE\tPLAYBOOK\tACTOR\tAGE\tNOTE")
	if len(view.Runs) == 0 {
		fmt.Fprintln(w, "(none)")
	}
	for _, r := range view.Runs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Issue, r.Phase, r.Playbook, r.Actor, time.Since(r.Started).Round(time.Minute), r.Note)
	}
	fmt.Fprintln(w, "\t\t\t\t\t")
	fmt.Fprintln(w, "READY\tPRIORITY\tPLAYBOOK\tTITLE\t\tPROBLEM")
	if len(view.Ready) == 0 {
		fmt.Fprintln(w, "(none)")
	}
	for _, i := range view.Ready {
		fmt.Fprintf(w, "%s\tP%d\t%s\t%s\t\t%s\n", i.ID, i.Priority, i.Playbook, i.Title, i.Problem)
	}
	return w.Flush()
}

func startFlags(name string) (*flag.FlagSet, *string, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	actor := fs.String("actor", "", "who is working (default $GAFFER_ACTOR, $BEADS_ACTOR, git user.name)")
	base := fs.String("base", "", "commit or branch to start from (default HEAD of the main checkout)")
	return fs, actor, base
}

func cmdNext(args []string) error {
	fs, actor, base := startFlags("next")
	var labels stringList
	fs.Var(&labels, "label", "only beads with this label (repeatable)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	a, err := open()
	if err != nil {
		return err
	}
	run, t, err := a.Next(labels, app.StartOptions{Actor: app.Actor(*actor), Base: *base})
	if err != nil {
		return &exitError{code: exitBlocked, msg: err.Error()}
	}
	fmt.Print(a.Brief(run, t, app.Interactive))
	fmt.Fprintf(os.Stderr, "\ngaffer: %s claimed; cd %s\n", run.Issue, run.Worktree)
	return nil
}

func cmdStart(args []string) error {
	fs, actor, base := startFlags("start")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return &exitError{code: exitUsage, msg: "usage: gaffer " + commands["start"].usage}
	}
	a, err := open()
	if err != nil {
		return err
	}
	run, t, err := a.Start(pos[0], app.StartOptions{Actor: app.Actor(*actor), Base: *base})
	if err != nil {
		return err
	}
	fmt.Print(a.Brief(run, t, app.Interactive))
	fmt.Fprintf(os.Stderr, "\ngaffer: %s claimed; cd %s\n", run.Issue, run.Worktree)
	return nil
}

func withRun(name string, args []string, setup func(*flag.FlagSet), fn func(a *app.App, run *work.Run) error) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	if setup != nil {
		setup(fs)
	}
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	a, err := open()
	if err != nil {
		return err
	}
	run, err := a.Resolve(one(pos))
	if err != nil {
		return err
	}
	return fn(a, run)
}

func cmdBrief(args []string) error {
	var review *bool
	return withRun("brief", args, func(fs *flag.FlagSet) {
		review = fs.Bool("review", false, "print the reviewer's brief")
	}, func(a *app.App, run *work.Run) error {
		t, err := a.Task(run.Issue)
		if err != nil {
			return err
		}
		if *review {
			fmt.Print(a.ReviewBrief(run, t))
		} else {
			fmt.Print(a.Brief(run, t, app.Interactive))
		}
		return nil
	})
}

func cmdLint(args []string) error {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	ids, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return &exitError{code: exitUsage, msg: "usage: gaffer " + commands["lint"].usage}
	}
	a, err := open()
	if err != nil {
		return err
	}
	bad := 0
	for _, id := range ids {
		problems, err := a.Lint(id)
		if err != nil {
			return err
		}
		if len(problems) == 0 {
			fmt.Printf("%s: ok\n", id)
			continue
		}
		bad++
		fmt.Printf("%s:\n  %s\n", id, strings.Join(problems, "\n  "))
	}
	if bad > 0 {
		return &exitError{code: exitFail}
	}
	return nil
}

func cmdVerify(args []string) error {
	var asJSON *bool
	return withRun("verify", args, func(fs *flag.FlagSet) {
		asJSON = fs.Bool("json", false, "print the verification record")
	}, func(a *app.App, run *work.Run) error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		v, err := a.Verify(ctx, run)
		if err != nil {
			return err
		}
		if *asJSON {
			if err := printJSON(v); err != nil {
				return err
			}
		} else {
			for _, c := range v.Checks {
				fmt.Printf("%-6s %s %s\n", c.Status, c.ID, c.Reason)
				if c.Status != work.Pass && c.Log != "" {
					fmt.Printf("       log: %s\n", c.Log)
				}
			}
			if len(v.Weakened) > 0 {
				fmt.Printf("note   verifiers changed since base (%s): independent review required\n", strings.Join(v.Weakened, ", "))
			}
			fmt.Printf("%s at %s\n", v.Status, v.Fingerprint)
		}
		switch v.Status {
		case work.Pass:
			return nil
		case work.Fail:
			return &exitError{code: exitFail}
		default:
			return &exitError{code: exitBlocked}
		}
	})
}

func cmdHandoff(args []string) error {
	return withRun("handoff", args, nil, func(a *app.App, run *work.Run) error {
		if err := a.Handoff(run); err != nil {
			return err
		}
		t, err := a.Task(run.Issue)
		if err != nil {
			return err
		}
		fmt.Print(a.ReviewBrief(run, t))
		fmt.Fprintf(os.Stderr, "\ngaffer: %s is in review. Hand the brief above to a reviewer on a different model.\n", run.Issue)
		return nil
	})
}

func cmdReview(args []string) error {
	var pass, fail *bool
	var notes, actor *string
	return withRun("review", args, func(fs *flag.FlagSet) {
		pass = fs.Bool("pass", false, "the change meets its acceptance criteria")
		fail = fs.Bool("fail", false, "the change does not meet its acceptance criteria")
		notes = fs.String("notes", "", "what you checked, or what must change")
		actor = fs.String("actor", "", "reviewer identity, e.g. reviewer:gpt-5")
	}, func(a *app.App, run *work.Run) error {
		if *pass == *fail {
			return &exitError{code: exitUsage, msg: "pass exactly one of --pass or --fail"}
		}
		if err := a.Review(run, app.Actor(*actor), *pass, *notes); err != nil {
			return err
		}
		fmt.Printf("%s: review recorded\n", run.Issue)
		return nil
	})
}

func cmdDone(args []string) error {
	return withRun("done", args, nil, func(a *app.App, run *work.Run) error {
		if err := a.Done(run); err != nil {
			return err
		}
		fmt.Printf("%s: closed. Branch %s holds the work; merging is up to you.\n", run.Issue, run.Branch)
		return nil
	})
}

func cmdBlock(args []string) error {
	var reason *string
	return withRun("block", args, func(fs *flag.FlagSet) {
		reason = fs.String("reason", "", "what a human must decide or provide")
	}, func(a *app.App, run *work.Run) error {
		if err := a.Block(run, *reason); err != nil {
			return err
		}
		fmt.Printf("%s: blocked\n", run.Issue)
		return nil
	})
}

func cmdResume(args []string) error {
	return withRun("resume", args, nil, func(a *app.App, run *work.Run) error {
		if err := a.Resume(run); err != nil {
			return err
		}
		fmt.Printf("%s: working in %s\n", run.Issue, run.Worktree)
		return nil
	})
}

func cmdAbandon(args []string) error {
	var reason *string
	return withRun("abandon", args, func(fs *flag.FlagSet) {
		reason = fs.String("reason", "", "why the run is ending")
	}, func(a *app.App, run *work.Run) error {
		if err := a.Abandon(run, *reason); err != nil {
			return err
		}
		fmt.Printf("%s: released; worktree kept at %s\n", run.Issue, run.Worktree)
		return nil
	})
}

func cmdDispatch(args []string) error {
	fs := flag.NewFlagSet("dispatch", flag.ContinueOnError)
	opt := app.DispatchOptions{Log: os.Stderr}
	fs.StringVar(&opt.Agent, "agent", "claude", "worker agent (claude, codex, or one from config.json)")
	fs.StringVar(&opt.Reviewer, "reviewer", "", "reviewer agent; empty leaves review to you")
	fs.IntVar(&opt.Max, "max", 2, "concurrent workers")
	fs.IntVar(&opt.Limit, "limit", 0, "total beads to start (0: until the queue drains)")
	fs.IntVar(&opt.Retries, "retries", 1, "extra attempts after a failed verification or review")
	fs.DurationVar(&opt.Heartbeat, "heartbeat", time.Minute, "lease refresh interval")
	var labels stringList
	fs.Var(&labels, "label", "only beads with this label (repeatable)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	opt.Labels = labels
	a, err := open()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return a.Dispatch(ctx, opt)
}

func cmdLesson(args []string) error {
	fs := flag.NewFlagSet("lesson", flag.ContinueOnError)
	global := fs.Bool("global", false, "apply to every project")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return &exitError{code: exitUsage, msg: "usage: gaffer " + commands["lesson"].usage}
	}
	path := filepath.Join(config.Home(), "standing.md")
	if !*global {
		a, err := open()
		if err != nil {
			return err
		}
		path = filepath.Join(a.Repo, config.StandingPath)
	}
	if err := config.AppendLesson(path, strings.Join(pos, " ")); err != nil {
		return err
	}
	fmt.Println("recorded in", path)
	return nil
}

func cmdPlaybooks(args []string) error {
	if len(args) == 1 {
		p, err := playbook.Get(args[0])
		if err != nil {
			return err
		}
		fmt.Print(p.Steps())
		return nil
	}
	for _, name := range playbook.Names() {
		p, _ := playbook.Get(name)
		fmt.Printf("%-12s review=%-5v read-only=%-5v types=%s labels=%s\n", p.Name, p.Review, p.ReadOnly, strings.Join(p.Types, ","), strings.Join(p.Labels, ","))
	}
	return nil
}

func cmdInstallSkill(args []string) error {
	fs := flag.NewFlagSet("install-skill", flag.ContinueOnError)
	home, _ := os.UserHomeDir()
	dir := fs.String("dir", filepath.Join(home, ".claude", "skills"), "skills directory")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	path := filepath.Join(*dir, "gaffer", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, gaffer.Skill, 0o644); err != nil {
		return err
	}
	fmt.Println("installed", path)
	return nil
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

var execCommand = exec.Command
