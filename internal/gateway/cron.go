package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/cron"
)

// CronRuntime is the scheduled-job surface behind the owner-only /cron
// command (SLASH-076). It is a separate interface so gateway.Runtime keeps its
// four session/status methods; serve implements both.
type CronRuntime interface {
	CronList(ctx context.Context) ([]cron.Job, error)
	CronSetEnabled(ctx context.Context, id string, enabled bool, reason string) error
	CronRemove(ctx context.Context, id string) error
	CronRun(ctx context.Context, id string) error
}

// CronEditor is the create/update half of the owner /cron surface.
type CronEditor interface {
	CronCreate(ctx context.Context, name, schedule, prompt string) (string, error)
	CronUpdate(ctx context.Context, id, field, value string) error
}

// splitCronSchedule finds the longest 5/6-field schedule prefix of
// "<schedule…> <prompt>" using the same parser as the scheduler.
func splitCronSchedule(rest string) (string, string, bool) {
	fields := strings.Fields(rest) // fields[0] is "add"
	for n := 6; n >= 5 && 1+n < len(fields); n-- {
		expr := strings.Join(fields[1:1+n], " ")
		if _, err := cron.ParseSchedule(expr, time.Local); err == nil {
			return expr, strings.Join(fields[1+n:], " "), true
		}
	}
	return "", "", false
}

// cronCommand implements SLASH-076: list, pause, resume, run and rm. It never
// starts a turn and never calls the LLM; the reply is the whole output.
func (c *Commands) cronCommand(ctx context.Context, _ *Ticket, cmd Command) CommandResult {
	if len(cmd.Args) > 0 && (cmd.Args[0] == "add" || cmd.Args[0] == "edit") {
		editor, ok := c.runtime.(CronEditor)
		if !ok {
			return unsupported("cron")
		}
		if cmd.Args[0] == "add" {
			return cronAdd(ctx, editor, cmd)
		}
		return cronEdit(ctx, editor, cmd)
	}
	cronRT, ok := c.runtime.(CronRuntime)
	if !ok {
		return unsupported("cron")
	}
	if len(cmd.Args) == 0 || cmd.Args[0] == "list" {
		return cronList(ctx, cronRT)
	}
	sub, id := cmd.Args[0], ""
	if len(cmd.Args) > 1 {
		id = cmd.Args[1]
	}
	if id == "" {
		return usage("cron " + sub + " <id>")
	}
	return cronMutate(ctx, cronRT, sub, id)
}

func cronMutate(ctx context.Context, cronRT CronRuntime, sub, id string) CommandResult {
	var err error
	switch sub {
	case "pause":
		err = cronRT.CronSetEnabled(ctx, id, false, "gateway pause")
	case "resume":
		err = cronRT.CronSetEnabled(ctx, id, true, "gateway resume")
	case "run":
		err = cronRT.CronRun(ctx, id)
	case "rm", "remove":
		err = cronRT.CronRemove(ctx, id)
	default:
		return usage("cron list|add|edit|pause|resume|run|rm")
	}
	if err != nil {
		return CommandResult{Handled: true, Reply: "cron " + sub + ": failed"}
	}
	return CommandResult{Handled: true, Reply: "cron " + sub + ": ok (" + id + ")"}
}

func cronAdd(ctx context.Context, editor CronEditor, cmd Command) CommandResult {
	schedule, prompt, ok := splitCronSchedule(cmd.Rest)
	if !ok {
		return usage("cron add <schedule> <prompt>")
	}
	id, err := editor.CronCreate(ctx, "", schedule, prompt)
	if err != nil {
		return CommandResult{Handled: true, Reply: "cron add: failed"}
	}
	return CommandResult{Handled: true, Reply: "cron add: ok (" + id + ")"}
}

func cronEdit(ctx context.Context, editor CronEditor, cmd Command) CommandResult {
	if len(cmd.Args) < 4 {
		return usage("cron edit <id> name|schedule|prompt <value>")
	}
	field := cmd.Args[2]
	if field != "name" && field != "schedule" && field != "prompt" {
		return usage("cron edit <id> name|schedule|prompt <value>")
	}
	if err := editor.CronUpdate(ctx, cmd.Args[1], field, strings.Join(cmd.Args[3:], " ")); err != nil {
		return CommandResult{Handled: true, Reply: "cron edit: failed"}
	}
	return CommandResult{Handled: true, Reply: "cron edit: ok (" + cmd.Args[1] + ")"}
}

func cronList(ctx context.Context, cronRT CronRuntime) CommandResult {
	jobs, err := cronRT.CronList(ctx)
	if err != nil {
		return CommandResult{Handled: true, Reply: "cron: unavailable"}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "cron: %d job(s)", len(jobs))
	for i := range jobs {
		j := &jobs[i]
		state := "paused"
		if j.Enabled {
			state = "active"
		}
		fmt.Fprintf(&b, "\n%s\t%s\t%s\t%s", j.ID, strings.Join(strings.Fields(j.Name), " "), j.Schedule, state)
	}
	return CommandResult{Handled: true, Reply: b.String()}
}

// splitCronSchedule finds the longest 5/6-field schedule prefix of
// "<schedule…> <prompt>" using the same parser as the scheduler.
