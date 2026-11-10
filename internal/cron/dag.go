//go:build !minimal

package cron

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DAG limits (GAP-REL-16): a job may depend on at most five upstream jobs in
// sequence, and a cycle is always invalid.
const (
	MaxDAGDepth = 5
)

// DAG errors are typed so the CLI can report invalid_args.
var (
	ErrDAGCycle   = errors.New("cron: after cycle")
	ErrDAGDepth   = errors.New("cron: after depth exceeds 5")
	ErrDAGUnknown = errors.New("cron: after names an unknown job")
)

// ValidateDAG rejects unknown references, cycles and chains deeper than
// [MaxDAGDepth]. Jobs are matched by ID or by name, whichever the reference
// spells exactly.
func ValidateDAG(jobs []Job) error {
	byRef := make(map[string]int, len(jobs))
	for i := range jobs {
		byRef[jobs[i].ID] = i
		byRef[jobs[i].Name] = i
	}
	edges := make([][]int, len(jobs))
	for i := range jobs {
		for _, ref := range jobs[i].After {
			j, ok := byRef[ref]
			if !ok {
				return fmt.Errorf("%w: %q", ErrDAGUnknown, ref)
			}
			edges[i] = append(edges[i], j)
		}
	}
	state := make([]uint8, len(jobs)) // 0 unvisited, 1 visiting, 2 done
	height := make([]int, len(jobs))  // longest upstream chain below i
	var visit func(i int) error
	visit = func(i int) error {
		switch state[i] {
		case 1:
			return fmt.Errorf("%w: %s", ErrDAGCycle, jobs[i].Name)
		case 2:
			return nil
		}
		state[i] = 1
		for _, next := range edges[i] {
			if err := visit(next); err != nil {
				return err
			}
			height[i] = max(height[i], height[next]+1)
		}
		if height[i] > MaxDAGDepth {
			return fmt.Errorf("%w: %s", ErrDAGDepth, jobs[i].Name)
		}
		state[i] = 2
		return nil
	}
	for i := range jobs {
		if err := visit(i); err != nil {
			return err
		}
	}
	return nil
}

// resolveAfter rewrites name references to job IDs so the runtime lookup and
// the store key agree. An unknown reference is left untouched; ValidateDAG is
// the gate that reports it.
func resolveAfter(jobs []Job) {
	byName := make(map[string]string, len(jobs))
	ids := make(map[string]bool, len(jobs))
	for i := range jobs {
		byName[jobs[i].Name] = jobs[i].ID
		ids[jobs[i].ID] = true
	}
	for i := range jobs {
		for k, ref := range jobs[i].After {
			if ids[ref] {
				continue
			}
			if id, ok := byName[ref]; ok {
				jobs[i].After[k] = id
			}
		}
	}
}

// upstream resolves the dependency context of j at slot at. ready is false
// while every dependency has not finished and the timeout has not passed;
// missing is true when the job must run without an upstream result.
func (s *Scheduler) upstream(ctx context.Context, j *Job, at time.Time) (text string, ready, missing bool, err error) {
	if len(j.After) == 0 {
		return "", true, false, nil
	}
	store, ok := s.Store.(UpstreamStore)
	if !ok {
		return "", false, false, errors.New("cron: store cannot resolve after dependencies")
	}
	timeout := j.AfterTimeout
	if timeout <= 0 {
		timeout = time.Hour
	}
	var parts []string
	for _, ref := range j.After {
		up, found, uerr := store.UpstreamRun(ctx, ref, at)
		if uerr != nil {
			return "", false, false, fmt.Errorf("upstream %s: %w", ref, uerr)
		}
		if !found {
			if s.clock().Now().Sub(at) < timeout {
				return "", false, false, nil
			}
			missing = true
			parts = append(parts, "upstream "+ref+"=upstream_missing")
			continue
		}
		age := s.clock().Now().Sub(up.FinishedAt)
		parts = append(parts, fmt.Sprintf("upstream %s execution_id=%s age=%s", ref, up.ExecutionID, age.Round(time.Second)))
	}
	return strings.Join(parts, "\n"), true, missing, nil
}
