package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/damnbot/damnbot/internal/cron"
	"github.com/damnbot/damnbot/internal/ctxdb"
)

// CronList, CronSetEnabled, CronRemove and CronRun implement gateway.Runtime
// for the owner-only /cron command over the shared cron.Admin.
func (g *serveGateway) CronList(ctx context.Context) ([]cron.Job, error) {
	jobs, err := g.cronAdmin.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("gateway cron list: %w", err)
	}
	return jobs, nil
}

func (g *serveGateway) CronSetEnabled(ctx context.Context, id string, enabled bool, reason string) error {
	if err := g.cronAdmin.SetEnabled(ctx, id, enabled, reason); err != nil {
		return fmt.Errorf("gateway cron set enabled: %w", err)
	}
	return nil
}

func (g *serveGateway) CronRemove(ctx context.Context, id string) error {
	if err := g.cronAdmin.Remove(ctx, id); err != nil {
		return fmt.Errorf("gateway cron remove: %w", err)
	}
	return nil
}

func (g *serveGateway) CronCreate(ctx context.Context, name, schedule, prompt string) (string, error) {
	if _, err := cron.ParseSchedule(schedule, time.Local); err != nil {
		return "", fmt.Errorf("gateway cron create: %w", err)
	}
	if name == "" {
		name = "gateway-" + schedule
	}
	got, _, err := g.cronAdmin.Create(ctx, &cron.Job{Name: name, Schedule: schedule, Prompt: prompt, Cwd: g.dataDir, Enabled: true})
	if err != nil {
		return "", fmt.Errorf("gateway cron create: %w", err)
	}
	return got.ID, nil
}

func (g *serveGateway) CronUpdate(ctx context.Context, id, field, value string) error {
	jobs, err := g.cronAdmin.List(ctx)
	if err != nil {
		return fmt.Errorf("gateway cron update: %w", err)
	}
	var job *cron.Job
	for i := range jobs {
		if jobs[i].ID == id {
			job = &jobs[i]
			break
		}
	}
	if job == nil {
		return fmt.Errorf("gateway cron update: job %s not found", id)
	}
	switch field {
	case "name":
		job.Name = value
	case "schedule":
		if _, err := cron.ParseSchedule(value, time.Local); err != nil {
			return fmt.Errorf("gateway cron update: %w", err)
		}
		job.Schedule = value
	case "prompt":
		job.Prompt = value
	default:
		return fmt.Errorf("gateway cron update: unknown field %s", field)
	}
	if err := g.cronAdmin.Update(ctx, job); err != nil {
		return fmt.Errorf("gateway cron update: %w", err)
	}
	return nil
}

func (g *serveGateway) CronRun(ctx context.Context, id string) error {
	if err := g.cronAdmin.Run(ctx, id); err != nil {
		return fmt.Errorf("gateway cron run: %w", err)
	}
	return nil
}

// wireCronAdmin builds the /cron surface from the gateway composition root.
func (g *serveGateway) wireCronAdmin(db *ctxdb.Store, stderr io.Writer) {
	g.cronAdmin = newCronAdmin(db, "serve", &cronPromptExecutor{
		configPath: g.configPath, dataDir: g.dataDir, model: g.modelRef, stderr: stderr,
	}, nil)
}
