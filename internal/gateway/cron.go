package gateway

import (
	"context"
	"fmt"
	"strings"

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

// cronCommand implements SLASH-076: list, pause, resume, run and rm. It never
// starts a turn and never calls the LLM; the reply is the whole output.
func (c *Commands) cronCommand(ctx context.Context, _ *Ticket, cmd Command) CommandResult {
	cronRT, ok := c.runtime.(CronRuntime)
	if !ok {
		return unsupported("cron")
	}
	if len(cmd.Args) == 0 || cmd.Args[0] == "list" {
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
	sub, id := cmd.Args[0], ""
	if len(cmd.Args) > 1 {
		id = cmd.Args[1]
	}
	if id == "" {
		return usage("cron " + sub + " <id>")
	}
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
		return usage("cron list|pause|resume|run|rm")
	}
	if err != nil {
		return CommandResult{Handled: true, Reply: "cron " + sub + ": failed"}
	}
	return CommandResult{Handled: true, Reply: "cron " + sub + ": ok (" + id + ")"}
}
