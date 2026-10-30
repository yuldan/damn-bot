package cron

import (
	"context"
	"fmt"
)

// Admin runs one explicit cron slot for the CLI (cron run) over the same
// scheduler the daemon uses.
type Admin struct {
	Store Store
	Exec  Executor
	Clock Clock
	Owner string
}

// Run executes one slot now, even for a paused job.
func (a *Admin) Run(ctx context.Context, id string) error {
	s := &Scheduler{Store: a.Store, Executor: a.Exec, Clock: a.Clock, Owner: a.Owner}
	if err := s.RunOnce(ctx, id); err != nil {
		return fmt.Errorf("cron admin: run: %w", err)
	}
	return nil
}
