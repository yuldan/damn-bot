package gwplat

import (
	"context"
	"errors"
	"fmt"

	"github.com/damnbot/damnbot/internal/gateway"
)

// ErrNoInbound rejects a dispatch without a handler or a message.
var ErrNoInbound = errors.New("gwplat: dispatch requires a handler and an inbound message")

// Handler receives one admitted inbound message. The gateway core implements
// admission; adapters only supply the authenticated envelope.
type Handler interface {
	Handle(ctx context.Context, in *gateway.Inbound) error
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, in *gateway.Inbound) error

// Handle implements Handler.
func (f HandlerFunc) Handle(ctx context.Context, in *gateway.Inbound) error { return f(ctx, in) }

// Dispatch applies the core mention policy of GAP-GW-05 with its default
// (groups require a mention, DMs always run) and only then calls h. Adapters
// populate Group/Mentioned/ReplyToBot/Command from the payload; message text
// is never re-parsed here for routing or authorization.
func Dispatch(ctx context.Context, h Handler, in *gateway.Inbound) error {
	if h == nil || in == nil {
		return ErrNoInbound
	}
	if !in.Triggers(true) {
		return nil
	}
	if err := h.Handle(ctx, in); err != nil {
		return fmt.Errorf("gwplat: dispatch %s: %w", in.Platform, err)
	}
	return nil
}
