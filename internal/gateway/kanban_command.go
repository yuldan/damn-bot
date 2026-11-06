package gateway

import (
	"context"
	"strings"

	"github.com/damnbot/damnbot/internal/gateway/render"
)

// Kanban is the board command seam (SLASH-080). The gateway owns the role
// check for mutations; the implementation owns the board operations.
type Kanban interface {
	Kanban(ctx context.Context, role Role, sessionID string, args []string) string
}

// kanbanCommand routes the owner/read split to the board seam. The reply is
// re-rendered through the platform renderer and bounded there; a board reply
// is never sent as a raw fmt string.
func (c *Commands) kanbanCommand(ctx context.Context, t *Ticket, cmd Command) CommandResult {
	if c.kanban == nil {
		return CommandResult{Handled: true, Reply: "kanban: module_unavailable: kanban is not wired"}
	}
	if len(cmd.Args) > 1 && cmd.Args[0] == "diag" {
		cmd.Args[0] = "diagnostics"
	}
	reply := c.kanban.Kanban(ctx, t.Identity.Role, t.Scope.Label(), cmd.Args)
	return CommandResult{Handled: true, Reply: limitChatReply(reply)}
}

// limitChatReply applies the platform renderer's chunk cap so a chat answer
// can never exceed the transport limit (SLASH-080).
func limitChatReply(reply string) string {
	msgs := render.RenderTelegram(reply, render.Limits{MaxLen: 3800, MaxChunks: 2})
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		parts = append(parts, m.Text)
	}
	return strings.Join(parts, "\n")
}
