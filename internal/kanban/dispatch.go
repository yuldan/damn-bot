package kanban

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// WorkerOutcome is what a worker reports for one claimed card.
type WorkerOutcome struct {
	Outcome string
	Handoff Handoff
	Run     *Run
}

// Runner executes one claimed task. It must not run shell on behalf of the
// board: the kanban tool surface never executes commands.
type Runner interface {
	Run(ctx context.Context, task *Task, worker string) (WorkerOutcome, error)
}

// Dispatcher claims ready cards and hands each to exactly one worker. The
// lease is the mutual-exclusion token: ClaimTask is a guarded UPDATE in one
// write transaction, so two dispatchers cannot own the same card.
type Dispatcher struct {
	Store         *Store
	Board         string
	Workers       []string
	Runner        Runner
	AutoDecompose bool
	Decomposer    Decomposer
	Log           func(string)
}

// DispatchOnce runs at most one task per worker and returns how many ran.
// Workers are tried in order; the first gets the highest-priority claim.
func (d *Dispatcher) DispatchOnce(ctx context.Context) (int, error) {
	if d.AutoDecompose && d.Decomposer != nil {
		if err := d.decomposeReady(ctx); err != nil {
			return 0, err
		}
	}
	ran := 0
	for _, worker := range d.Workers {
		if err := ctx.Err(); err != nil {
			return ran, fmt.Errorf("dispatch cancelled: %w", err)
		}
		ok, err := d.runWorker(ctx, worker)
		if err != nil || !ok {
			return ran, err
		}
		ran++
	}
	return ran, nil
}

// runWorker claims and executes one card. ok=false is a clean empty board.
func (d *Dispatcher) runWorker(ctx context.Context, worker string) (bool, error) {
	t, err := d.Store.Claim(ctx, d.Board, worker, 0)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if d.Runner == nil {
		if _, rerr := d.Store.Reclaim(ctx, t.ID, worker, true); rerr != nil {
			return false, rerr
		}
		return false, fmt.Errorf("%w: runner is required", ErrNotConfigured)
	}
	out, err := d.Runner.Run(ctx, t, worker)
	if err == nil {
		_, err = d.Store.Complete(ctx, t.ID, &out.Handoff, out.Run, worker)
	}
	if err != nil {
		if _, rerr := d.Store.Reclaim(ctx, t.ID, worker, true); rerr != nil {
			return false, errors.Join(err, rerr)
		}
		return false, err
	}
	return true, nil
}

// Run loops until ctx ends. interval defaults to 30s.
func (d *Dispatcher) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	for {
		if _, err := d.DispatchOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			if d.Log != nil {
				d.Log(err.Error())
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (d *Dispatcher) decomposeReady(ctx context.Context) error {
	tasks, err := d.Store.Tasks(ctx, d.Board, Filter{Status: StatusReady, Limit: 0})
	if err != nil {
		return err
	}
	for i := range tasks {
		t := &tasks[i]
		if !strings.Contains(strings.Join(t.Labels, ","), "decompose") || len(t.Runs) > 0 {
			continue
		}
		if _, err := d.Store.Decompose(ctx, t.ID, d.Decomposer, 8); err != nil && !errors.Is(err, ErrNotConfigured) {
			return err
		}
	}
	return nil
}

// RunnerFunc adapts a function to Runner (used by cmd and tests).
type RunnerFunc func(ctx context.Context, task *Task, worker string) (WorkerOutcome, error)

// Run implements Runner.
func (f RunnerFunc) Run(ctx context.Context, task *Task, worker string) (WorkerOutcome, error) {
	return f(ctx, task, worker)
}
