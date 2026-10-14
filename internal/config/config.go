// Package config resolves gaffer's file locations and agent commands.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StandingPath holds a project's standing orders, committed with the code.
const StandingPath = ".gaffer/standing.md"

// Home is gaffer's per-user directory (~/.config/gaffer by default).
func Home() string {
	if d := os.Getenv("GAFFER_HOME"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "gaffer")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "gaffer")
}

// WorktreeRoot is where run worktrees live, outside every repository.
func WorktreeRoot() string {
	if d := os.Getenv("GAFFER_WORKTREES"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".gaffer", "worktrees")
}

// Agent is a headless coding agent command. The brief arrives on stdin.
// "{repo}" and "{worktree}" in any argument expand to the main checkout and
// the run's worktree.
type Agent struct {
	Command []string `json:"command"`
}

// Expand substitutes the placeholders for one run.
func (a Agent) Expand(repo, worktree string) []string {
	out := make([]string, len(a.Command))
	r := strings.NewReplacer("{repo}", repo, "{worktree}", worktree)
	for i, arg := range a.Command {
		out[i] = r.Replace(arg)
	}
	return out
}

// File is ~/.config/gaffer/config.json.
type File struct {
	Agents map[string]Agent `json:"agents"`
}

// builtin agents run unattended inside a worktree. Override or add agents in
// config.json; the brief is always written to the agent's stdin. Agents also
// write to the main checkout, because git keeps a worktree's commits in the
// shared .git directory and beads lives there too.
var builtin = map[string]Agent{
	"claude": {Command: []string{"claude", "-p", "--permission-mode", "acceptEdits", "--allowedTools", "Bash,Read,Edit,Write,Glob,Grep", "--add-dir", "{repo}"}},
	"codex":  {Command: []string{"codex", "exec", "--sandbox", "workspace-write", "--add-dir", "{repo}"}},
}

// LoadAgent returns the named agent from config.json, then the builtins.
func LoadAgent(name string) (Agent, error) {
	var f File
	raw, err := os.ReadFile(filepath.Join(Home(), "config.json"))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return Agent{}, err
	default:
		if err := json.Unmarshal(raw, &f); err != nil {
			return Agent{}, fmt.Errorf("%s: %w", filepath.Join(Home(), "config.json"), err)
		}
	}
	if a, ok := f.Agents[name]; ok && len(a.Command) > 0 {
		return a, nil
	}
	if a, ok := builtin[name]; ok {
		return a, nil
	}
	return Agent{}, fmt.Errorf("unknown agent %q: add it under \"agents\" in %s", name, filepath.Join(Home(), "config.json"))
}

// Standing returns the global then project standing orders, concatenated.
func Standing(repoRoot string) string {
	var parts []string
	for _, p := range []string{filepath.Join(Home(), "standing.md"), filepath.Join(repoRoot, StandingPath)} {
		if b, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(b)) != "" {
			parts = append(parts, strings.TrimSpace(string(b)))
		}
	}
	return strings.Join(parts, "\n")
}

// AppendLesson adds one numbered-by-position rule to a standing orders file.
func AppendLesson(path, lesson string) error {
	lesson = strings.TrimSpace(lesson)
	if lesson == "" || strings.Contains(lesson, "\n") {
		return errors.New("a lesson is one non-empty line")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(strings.TrimPrefix(line, "- ")) == lesson {
			return nil
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		fmt.Fprintln(f)
	}
	_, err = fmt.Fprintf(f, "- %s\n", lesson)
	return err
}
