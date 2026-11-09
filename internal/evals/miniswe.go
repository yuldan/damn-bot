package evals

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"

	"github.com/damnbot/damnbot/internal/batch"
)

// SWETask is one mini SWE subset task: a base tree and the wanted files after
// the fix.
type SWETask struct {
	ID    string            `json:"id"`
	Issue string            `json:"issue"`
	Files map[string]string `json:"files"`
	Want  map[string]string `json:"want"`
}

// Patch is a solver's proposed file contents; a file not present is kept.
type Patch struct {
	Files map[string]string `json:"files"`
}

// Solver proposes a patch for one task; tests inject a fake solver.
type Solver interface {
	Solve(ctx context.Context, t SWETask) (Patch, error)
}

// SWEResult is the deterministic mini SWE score and the per-task Hermes
// trajectory records.
type SWEResult struct {
	Tasks        int                `json:"tasks"`
	Passed       int                `json:"passed"`
	Trajectories []batch.Trajectory `json:"trajectories"`
}

// ErrBadTask rejects a task without an id, base file, or wanted tree.
var ErrBadTask = errors.New("evals: invalid swe task")

var sweID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// Subset returns the tasks named by ids in task-list order; an unknown id is
// an error (the subset is never silently narrowed).
func Subset(tasks []SWETask, ids []string) ([]SWETask, error) {
	if len(ids) == 0 {
		return slices.Clone(tasks), nil
	}
	keep := map[string]bool{}
	for _, id := range ids {
		keep[id] = true
	}
	var out []SWETask
	for _, t := range tasks {
		if keep[t.ID] {
			out = append(out, t)
			delete(keep, t.ID)
		}
	}
	if len(keep) > 0 {
		return nil, fmt.Errorf("evals: unknown swe task ids")
	}
	return out, nil
}

// RunMiniSWE applies each patch over the base tree and compares the result
// with Want. It needs no network, container, or model call; the solver seam
// is the only external input.
func RunMiniSWE(ctx context.Context, tasks []SWETask, s Solver) (SWEResult, error) {
	if s == nil {
		return SWEResult{}, errors.New("evals: solver required")
	}
	res := SWEResult{Tasks: len(tasks)}
	for _, t := range tasks {
		if err := validateTask(t); err != nil {
			return SWEResult{}, err
		}
		patch, err := s.Solve(ctx, t)
		got := maps.Clone(t.Files)
		ok := err == nil && maps.Equal(apply(got, patch.Files), t.Want)
		if ok {
			res.Passed++
		}
		res.Trajectories = append(res.Trajectories, batch.Trajectory{ID: t.ID, Steps: []batch.Step{
			{Role: "system", Content: "mini-swe"},
			{Role: "user", Content: t.Issue},
			{Role: "assistant", Content: outcome(ok)},
		}})
	}
	return res, nil
}

func validateTask(t SWETask) error {
	if !sweID.MatchString(t.ID) || len(t.Files) == 0 || len(t.Want) == 0 {
		return fmt.Errorf("%w: %q", ErrBadTask, t.ID)
	}
	return nil
}

func apply(base, patch map[string]string) map[string]string {
	if base == nil {
		base = map[string]string{}
	}
	maps.Copy(base, patch)
	return base
}

func outcome(ok bool) string {
	if ok {
		return "pass"
	}
	return "fail"
}
