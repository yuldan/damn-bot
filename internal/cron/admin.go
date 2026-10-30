package cron

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// COST-14 guard errors (mapped to invalid_args by the tool).
var (
	ErrMinInterval = errors.New("cron: schedule is more frequent than min_interval")
	ErrJobQuota    = errors.New("cron: active job quota reached")
)

// Limits is the COST-14 guard; zero values mean 15 minutes and 20 jobs.
type Limits struct {
	MinInterval time.Duration
	MaxJobs     int
}

// ActiveJobs counts enabled jobs of one owner; the composition root backs it
// with cron_jobs.
type ActiveJobs interface {
	ActiveJobs(ctx context.Context, owner string) (int, error)
}

// AdminStore is the management storage: the scheduler store plus the job
// definition mutations. cronstore backs it in the composition root.
type AdminStore interface {
	Store
	Create(ctx context.Context, j *Job) (Job, bool, error)
	Update(ctx context.Context, j *Job) error
	SetEnabled(ctx context.Context, id string, enabled bool, reason string) error
	Remove(ctx context.Context, id string) error
}

// Admin is the one management seam of the cron CLI, cronjob_manage and /cron;
// Run claims and executes one explicit slot through the same Scheduler.
type Admin struct {
	Store  AdminStore
	Exec   Executor
	Clock  Clock
	Owner  string
	Limits Limits
	Gates  GateRunner
}

// List returns every job definition.
func (a *Admin) List(ctx context.Context) ([]Job, error) {
	jobs, err := a.Store.Jobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("cron admin: list: %w", err)
	}
	return jobs, nil
}

// Create inserts j and reports whether it was new (COST-14 dedup).
func (a *Admin) Create(ctx context.Context, j *Job) (Job, bool, error) {
	if j.Owner == "" {
		j.Owner = a.Owner
	}
	if err := a.guard(ctx, j); err != nil {
		return Job{}, false, err
	}
	got, created, err := a.Store.Create(ctx, j)
	if err != nil {
		return Job{}, false, fmt.Errorf("cron admin: create: %w", err)
	}
	return got, created, nil
}

// Update rewrites the mutable definition of one job.
func (a *Admin) Update(ctx context.Context, j *Job) error {
	if err := a.minInterval(j); err != nil {
		return err
	}
	if err := a.Store.Update(ctx, j); err != nil {
		return fmt.Errorf("cron admin: update: %w", err)
	}
	return nil
}

// guard enforces the COST-14 interval and active-job quota before an insert.
func (a *Admin) guard(ctx context.Context, j *Job) error {
	if err := a.minInterval(j); err != nil {
		return err
	}
	maxJobs := a.Limits.MaxJobs
	if maxJobs <= 0 {
		maxJobs = 20
	}
	counts, ok := a.Store.(ActiveJobs)
	if !ok {
		return nil
	}
	n, err := counts.ActiveJobs(ctx, j.Owner)
	if err != nil {
		return fmt.Errorf("cron admin: count jobs: %w", err)
	}
	if n >= maxJobs {
		return fmt.Errorf("%w: %d active (max %d) for %s", ErrJobQuota, n, maxJobs, j.Owner)
	}
	return nil
}

// minInterval rejects slots closer than the configured minimum.
func (a *Admin) minInterval(j *Job) error {
	minInterval := a.Limits.MinInterval
	if minInterval <= 0 {
		minInterval = 15 * time.Minute
	}
	loc := time.Local
	if j.TZ != "" {
		l, err := time.LoadLocation(j.TZ)
		if err != nil {
			return fmt.Errorf("cron admin: tz %q: %w", j.TZ, err)
		}
		loc = l
	}
	sched, err := ParseSchedule(j.Schedule, loc)
	if err != nil {
		return fmt.Errorf("cron admin: %w", err)
	}
	now := time.Now()
	if a.Clock != nil {
		now = a.Clock.Now()
	}
	first := sched.Next(now)
	if first.IsZero() {
		return nil
	}
	next := first
	for range 3 {
		t := sched.Next(next)
		if t.IsZero() {
			break
		}
		if gap := t.Sub(next); gap < minInterval {
			return fmt.Errorf("%w: %s fires every %s (min %s)", ErrMinInterval, j.Schedule, gap, minInterval)
		}
		next = t
	}
	return nil
}

// SetEnabled pauses or resumes one job.
func (a *Admin) SetEnabled(ctx context.Context, id string, enabled bool, reason string) error {
	if err := a.Store.SetEnabled(ctx, id, enabled, reason); err != nil {
		return fmt.Errorf("cron admin: set enabled: %w", err)
	}
	return nil
}

// Remove deletes a job and its run history.
func (a *Admin) Remove(ctx context.Context, id string) error {
	if err := a.Store.Remove(ctx, id); err != nil {
		return fmt.Errorf("cron admin: remove: %w", err)
	}
	return nil
}

// Run executes one slot now, even for a paused job.
func (a *Admin) Run(ctx context.Context, id string) error { return a.run(ctx, id) }

func (a *Admin) run(ctx context.Context, id string) error {
	s := &Scheduler{Store: a.Store, Executor: a.Exec, Clock: a.Clock, Owner: a.Owner, Gates: a.Gates}
	if err := s.RunOnce(ctx, id); err != nil {
		return fmt.Errorf("cron admin: run: %w", err)
	}
	return nil
}
