//go:build !minimal

package auto

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// TickResult is one due task run, including its error. The scheduler is the
// non-critical lane: a failing task never stops the loop or a turn.
type TickResult struct {
	Feature Feature
	Err     error
}

type scheduled struct {
	every time.Duration
	fn    func(context.Context) error
	next  time.Time
}

// Scheduler runs opt-in periodic tasks with an injected clock. Callers own
// the goroutine: [Scheduler.Run] returns as soon as ctx is cancelled, so a
// stopped process leaves no timer or goroutine behind.
type Scheduler struct {
	mu    sync.Mutex
	now   func() time.Time
	tasks map[Feature]scheduled
}

// NewScheduler returns a scheduler; now defaults to time.Now.
func NewScheduler(now func() time.Time) *Scheduler {
	if now == nil {
		now = time.Now
	}
	return &Scheduler{now: now, tasks: map[Feature]scheduled{}}
}

// Every registers (or replaces) the periodic task of f. A non-positive
// interval is rejected; the feature must exist in the closed registry.
func (s *Scheduler) Every(f Feature, every time.Duration, fn func(context.Context) error) error {
	if _, ok := Lookup(f); !ok {
		return fmt.Errorf("%w: %q", ErrUnknownFeature, f)
	}
	if every <= 0 {
		return fmt.Errorf("%w: %s interval must be > 0", ErrConfig, f)
	}
	if fn == nil {
		return fmt.Errorf("%w: %s task is nil", ErrConfig, f)
	}
	s.mu.Lock()
	s.tasks[f] = scheduled{every: every, fn: fn, next: s.now().Add(every)}
	s.mu.Unlock()
	return nil
}

// Tick runs every due task once, in registry order, and returns the results.
// It never sleeps and never starts a goroutine.
func (s *Scheduler) Tick(ctx context.Context) []TickResult {
	s.mu.Lock()
	now := s.now()
	var due []Feature
	for _, d := range definitions() {
		t, ok := s.tasks[d.Feature]
		if ok && !now.Before(t.next) {
			due = append(due, d.Feature)
		}
	}
	jobs := make([]scheduled, 0, len(due))
	for _, f := range due {
		t := s.tasks[f]
		t.next = now.Add(t.every)
		s.tasks[f] = t
		jobs = append(jobs, t)
	}
	s.mu.Unlock()

	out := make([]TickResult, 0, len(due))
	for i, f := range due {
		out = append(out, TickResult{Feature: f, Err: jobs[i].fn(ctx)})
	}
	return out
}

// Run ticks until ctx is cancelled. Cancellation is a clean stop (nil), not
// an error; a task error is reported but does not stop the loop.
func (s *Scheduler) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("%w: tick interval must be > 0", ErrConfig)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			s.Tick(ctx)
		}
	}
}

// Pending reports the number of registered tasks (diagnostics/tests).
func (s *Scheduler) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tasks)
}
