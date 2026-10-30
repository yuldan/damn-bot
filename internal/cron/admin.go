package cron

import (
	"context"
	"fmt"
)

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
	Store AdminStore
	Exec  Executor
	Clock Clock
	Owner string
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
	got, created, err := a.Store.Create(ctx, j)
	if err != nil {
		return Job{}, false, fmt.Errorf("cron admin: create: %w", err)
	}
	return got, created, nil
}

// Update rewrites the mutable definition of one job.
func (a *Admin) Update(ctx context.Context, j *Job) error {
	if err := a.Store.Update(ctx, j); err != nil {
		return fmt.Errorf("cron admin: update: %w", err)
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
	s := &Scheduler{Store: a.Store, Executor: a.Exec, Clock: a.Clock, Owner: a.Owner}
	if err := s.RunOnce(ctx, id); err != nil {
		return fmt.Errorf("cron admin: run: %w", err)
	}
	return nil
}
