package main

import (
	"context"
	"fmt"

	"github.com/damnbot/damnbot/internal/cron"
)

// CronList, CronSetEnabled, CronRemove and CronRun implement gateway.Runtime
// for the owner-only /cron command over the shared cron.Admin.
func (g *serveGateway) CronList(ctx context.Context) ([]cron.Job, error) {
	jobs, err := g.cron.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("gateway cron list: %w", err)
	}
	return jobs, nil
}

func (g *serveGateway) CronSetEnabled(ctx context.Context, id string, enabled bool, reason string) error {
	if err := g.cron.SetEnabled(ctx, id, enabled, reason); err != nil {
		return fmt.Errorf("gateway cron set enabled: %w", err)
	}
	return nil
}

func (g *serveGateway) CronRemove(ctx context.Context, id string) error {
	if err := g.cron.Remove(ctx, id); err != nil {
		return fmt.Errorf("gateway cron remove: %w", err)
	}
	return nil
}

func (g *serveGateway) CronRun(ctx context.Context, id string) error {
	if err := g.cron.Run(ctx, id); err != nil {
		return fmt.Errorf("gateway cron run: %w", err)
	}
	return nil
}
