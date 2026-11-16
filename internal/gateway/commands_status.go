package gateway

import (
	"context"
	"fmt"
	"time"
)

// status implements the owner-only "/status" of SELF-HEALING: uptime, queue
// depth, active turns, mode and version. It reads runtime state only.
func (c *Commands) status(ctx context.Context, t *Ticket, _ Command) CommandResult {
	if c.runtime == nil {
		return unsupported("status")
	}
	st, err := c.runtime.Status(ctx)
	if err != nil {
		return CommandResult{Handled: true, Reply: "status unavailable"}
	}
	version := st.Version
	if version == "" {
		version = "unknown"
	}
	home, stop := "", "off"
	if st.Home != "" {
		home = ", home " + st.Home
	}
	if st.Paused {
		stop = "on (" + st.PauseReason + ")"
	}
	waiting := ""
	if st.WaitingProvider >= ProviderWaitVisible {
		waiting = fmt.Sprintf(", menunggu provider %d s", int64(st.WaitingProvider/time.Second))
	}
	tzName := "host"
	if zones, ok := c.runtime.(TimeZones); ok && t != nil {
		if zone, zerr := zones.Timezone(ctx, t.Scope.Platform, t.Scope.UserID); zerr == nil && zone != "" {
			tzName = zone
		}
	}
	reply := fmt.Sprintf(
		"status: up %s, queue %d, active turns %d, emergency stop %s, mode %s, version %s%s%s, tz %s",
		st.Uptime.Truncate(time.Second), st.QueueDepth, st.ActiveTurns, stop, modeName(st.SafeMode), version, home, waiting, tzName)
	return CommandResult{Handled: true, Reply: c.sessionStatusReply(ctx, t, reply)}
}
