// Package verify loads a project's verifier registry and runs checks.
package verify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"syscall"
	"time"

	"github.com/cduggn/gaffer/internal/work"
)

// RegistryPath is where a project registers its verifiers, relative to the
// repository root. It is committed with the code it checks.
const RegistryPath = ".gaffer/verifiers.json"

// Verifier proves one named claim by running one command.
type Verifier struct {
	ID      string   `json:"id"`
	Claim   string   `json:"claim"`
	Command []string `json:"command"`
	Timeout float64  `json:"timeout_seconds"`
	Scope   string   `json:"scope,omitempty"`
}

// Registry is the parsed verifiers file. Defaults run for every task.
type Registry struct {
	Defaults  []string   `json:"defaults"`
	Verifiers []Verifier `json:"verifiers"`
	byID      map[string]Verifier
}

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// Load reads and validates the registry under root. A missing file is an
// empty registry, so projects can start with review-only criteria.
func Load(root string) (*Registry, error) {
	raw, err := os.ReadFile(filepath.Join(root, RegistryPath))
	if errors.Is(err, os.ErrNotExist) {
		return Parse([]byte(`{"verifiers":[]}`))
	}
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

func Parse(raw []byte) (*Registry, error) {
	var r Registry
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", RegistryPath, err)
	}
	r.byID = map[string]Verifier{}
	for i, v := range r.Verifiers {
		switch {
		case !validID.MatchString(v.ID):
			return nil, fmt.Errorf("%s: verifier %d has invalid id %q", RegistryPath, i, v.ID)
		case r.byID[v.ID].ID != "":
			return nil, fmt.Errorf("%s: duplicate verifier id %s", RegistryPath, v.ID)
		case len(v.Command) == 0:
			return nil, fmt.Errorf("%s: verifier %s has no command", RegistryPath, v.ID)
		case !(v.Timeout > 0) || math.IsInf(v.Timeout, 0):
			return nil, fmt.Errorf("%s: verifier %s needs a positive timeout_seconds", RegistryPath, v.ID)
		}
		r.byID[v.ID] = v
	}
	for _, id := range r.Defaults {
		if _, ok := r.byID[id]; !ok {
			return nil, fmt.Errorf("%s: default %s is not registered", RegistryPath, id)
		}
	}
	return &r, nil
}

func (r *Registry) Get(id string) (Verifier, bool) {
	v, ok := r.byID[id]
	return v, ok
}

// Fingerprinter reports the identity of the tree under test.
type Fingerprinter func(dir string) (work.Fingerprint, error)

// Run executes every id in order inside dir, writing one log per check to
// logDir. The verification passes only if every check passes and the tree is
// byte-identical before and after: a check that edits the tree proves nothing.
func Run(ctx context.Context, reg *Registry, dir, logDir string, ids []string, fp Fingerprinter) (work.Verification, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return work.Verification{}, err
	}
	before, err := fp(dir)
	if err != nil {
		return work.Verification{}, err
	}
	v := work.Verification{Fingerprint: before, Started: time.Now().UTC(), Status: work.Pass}
	for _, id := range ids {
		res := runOne(ctx, reg, id, dir, logDir)
		v.Checks = append(v.Checks, res)
		v.Status = worst(v.Status, res.Status)
	}
	after, err := fp(dir)
	if err != nil {
		return v, err
	}
	if after != before {
		v.Status = work.BlockedStatus
		v.Checks = append(v.Checks, work.CheckResult{
			ID: "tree-unchanged", Status: work.BlockedStatus,
			Reason: fmt.Sprintf("checks modified the tree: %s -> %s", before, after),
		})
	}
	if len(ids) == 0 {
		v.Status = work.Pass
	}
	v.Finished = time.Now().UTC()
	return v, nil
}

func worst(a, b work.Status) work.Status {
	rank := map[work.Status]int{work.Pass: 0, work.Fail: 1, work.BlockedStatus: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func runOne(ctx context.Context, reg *Registry, id, dir, logDir string) work.CheckResult {
	ver, ok := reg.Get(id)
	if !ok {
		return work.CheckResult{ID: id, Status: work.BlockedStatus, Reason: "not registered in " + RegistryPath}
	}
	res := work.CheckResult{ID: id, Claim: ver.Claim, Command: ver.Command, Log: filepath.Join(logDir, id+".log")}
	logf, err := os.Create(res.Log)
	if err != nil {
		res.Status, res.Reason = work.BlockedStatus, err.Error()
		return res
	}
	defer logf.Close()

	ctx, cancel := context.WithTimeout(ctx, time.Duration(ver.Timeout*float64(time.Second)))
	defer cancel()
	cmd := exec.Command(ver.Command[0], ver.Command[1:]...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		res.Status, res.Reason = work.BlockedStatus, err.Error()
		return res
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		res.Seconds = time.Since(start).Seconds()
		res.Status, res.Reason = work.BlockedStatus, "timed out after "+time.Duration(ver.Timeout*float64(time.Second)).String()
		return res
	}
	// Checks must not leave children behind; reap the whole group.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	res.Seconds = time.Since(start).Seconds()
	code := cmd.ProcessState.ExitCode()
	res.ExitCode = &code
	if waitErr == nil && code == 0 {
		res.Status = work.Pass
	} else {
		res.Status = work.Fail
	}
	return res
}

// Weakened lists verifiers registered at base that current removed, changed,
// or dropped from defaults. Adding verifiers never weakens.
func Weakened(base, current *Registry) []string {
	var out []string
	defaults := map[string]bool{}
	for _, id := range current.Defaults {
		defaults[id] = true
	}
	baseDefault := map[string]bool{}
	for _, id := range base.Defaults {
		baseDefault[id] = true
	}
	for _, b := range base.Verifiers {
		c, ok := current.Get(b.ID)
		switch {
		case !ok:
			out = append(out, b.ID+" removed")
		case !slices.Equal(b.Command, c.Command) || b.Timeout != c.Timeout:
			out = append(out, b.ID+" changed")
		case baseDefault[b.ID] && !defaults[b.ID]:
			out = append(out, b.ID+" no longer default")
		}
	}
	return out
}
