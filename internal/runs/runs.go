// Package runs persists run records and their evidence under the repository's
// shared git directory, one directory per run so no two runs share a file.
package runs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/cduggn/gaffer/internal/work"
)

// Store is the run directory: <git-common-dir>/gaffer/runs.
type Store struct{ Dir string }

func Open(commonDir string) Store { return Store{Dir: filepath.Join(commonDir, "gaffer", "runs")} }

// NewID is sortable by start time and unique across concurrent starters.
func NewID(now time.Time) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return now.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b)
}

func (s Store) RunDir(id string) string { return filepath.Join(s.Dir, id) }

func (s Store) Save(r *work.Run) error {
	return writeJSON(filepath.Join(s.RunDir(r.ID), "run.json"), r)
}

func (s Store) Load(id string) (*work.Run, error) {
	var r work.Run
	if err := readJSON(filepath.Join(s.RunDir(id), "run.json"), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// All returns every run, oldest first.
func (s Store) All() ([]*work.Run, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*work.Run
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		r, err := s.Load(e.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Active returns the newest non-terminal run for an issue, or nil.
func (s Store) Active(issue string) (*work.Run, error) {
	all, err := s.All()
	if err != nil {
		return nil, err
	}
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Issue == issue && !all[i].Phase.Terminal() {
			return all[i], nil
		}
	}
	return nil, nil
}

// ByWorktree finds the active run whose worktree is dir.
func (s Store) ByWorktree(dir string) (*work.Run, error) {
	all, err := s.All()
	if err != nil {
		return nil, err
	}
	for i := len(all) - 1; i >= 0; i-- {
		if !all[i].Phase.Terminal() && sameDir(all[i].Worktree, dir) {
			return all[i], nil
		}
	}
	return nil, nil
}

func sameDir(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

func (s Store) SaveVerification(v *work.Verification) error {
	return writeJSON(filepath.Join(s.RunDir(v.Run), "verifications", v.ID+".json"), v)
}

// LatestVerification is the most recent verification for a run, or nil.
func (s Store) LatestVerification(run string) (*work.Verification, error) {
	dir := filepath.Join(s.RunDir(run), "verifications")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Each verification is <id>.json beside an <id>/ log directory that
	// exists while it runs; only finished records count.
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	sort.Strings(names)
	var v work.Verification
	if err := readJSON(filepath.Join(dir, names[len(names)-1]), &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func (s Store) SaveVerdict(run string, v *work.Verdict) error {
	return writeJSON(filepath.Join(s.RunDir(run), "verdict.json"), v)
}

func (s Store) Verdict(run string) (*work.Verdict, error) {
	var v work.Verdict
	err := readJSON(filepath.Join(s.RunDir(run), "verdict.json"), &v)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// writeJSON replaces path atomically so a crash never leaves a torn record.
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
