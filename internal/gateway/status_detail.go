//go:build !minimal

package gateway

import (
	"context"
	"fmt"
)

// StatusDetail is the optional per-session detail of /status (SLASH-038).
// Implementations report the caller's own session; the base Runtime status
// stays machine-wide.
type StatusDetail interface {
	SessionStatus(ctx context.Context, scope *Scope) (SessionStatus, error)
}

// SessionStatus is the session/model/token/context block /status appends.
// SessionID empty means the route has no session yet.
type SessionStatus struct {
	SessionID    string
	Model        string
	InputTokens  int64
	OutputTokens int64
	ContextUsed  int64
	ContextLimit int64
}

// sessionStatusReply appends the caller's session, model, ledger tokens, and
// context window when the runtime implements StatusDetail. A route without a
// session, or a detail error, keeps the machine-wide reply intact.
func (c *Commands) sessionStatusReply(ctx context.Context, t *Ticket, reply string) string {
	detail, ok := c.runtime.(StatusDetail)
	if !ok || t == nil {
		return reply
	}
	ss, err := detail.SessionStatus(ctx, &t.Scope)
	if err != nil || ss.SessionID == "" {
		return reply
	}
	model := ss.Model
	if model == "" {
		model = "unknown"
	}
	return reply + fmt.Sprintf("; session %s, model %s, tokens %d/%d, context %d/%d",
		ss.SessionID, model, ss.InputTokens, ss.OutputTokens, ss.ContextUsed, ss.ContextLimit)
}
