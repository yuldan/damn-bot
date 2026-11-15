//go:build minimal

package gateway

import "context"

// sessionStatusReply keeps the machine-wide /status reply in the minimal build;
// the per-session detail is excluded from the size-gated binary (P13).
func (c *Commands) sessionStatusReply(_ context.Context, _ *Ticket, reply string) string {
	return reply
}
