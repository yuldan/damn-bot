package main

import (
	"context"
	"fmt"
	"strconv"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/hooks"
	"github.com/damnbot/damnbot/internal/loop/event"
)

// gatewayInboundHooks adapts hooks to gateway (hooks cannot import gateway).
type gatewayInboundHooks struct{ reg *hooks.Registry }

// Inbound fires gateway_inbound; only an explicit allow admits (nil = none).
func (h gatewayInboundHooks) Inbound(ctx context.Context, in *gateway.Inbound) (gateway.InboundDecision, error) {
	if h.reg == nil {
		return gateway.InboundDecision{Allow: true}, nil
	}
	v, err := h.reg.Fire(ctx, hooks.EventGatewayInbound, map[string]any{
		"platform": in.Platform, "chat_id": in.ChatID, "user_id": in.SenderID, "text": in.Text,
	})
	d := gateway.InboundDecision{Hook: v.Hook}
	if err != nil {
		return d, fmt.Errorf("gateway_inbound: %w", err)
	}
	d.Allow, d.Reason = v.Decision == hooks.DecisionAllow, v.Reason
	if !d.Allow && d.Reason == "" {
		d.Reason = string(v.Decision)
	}
	return d, nil
}

// recordHookError is the fail-closed audit path of SPEC §4.10: the update is
// rejected (zero LLM), the error is logged, and one hook_error event is
// appended to the system session. Logging never includes message text.
func (g *serveGateway) recordHookError(ctx context.Context, out *gateway.Outcome) {
	sessionsPrintf(g.stderr, "gateway: hook %s failed: %s\n", cleanArg(out.Hook), cleanLine(out.HookDetail))
	payload := `{"hook":` + strconv.Quote(out.Hook) + `,"event":"gateway_inbound","error":` + strconv.Quote(out.HookDetail) + `}`
	if _, err := g.store.DB.Append(ctx, ctxdb.SystemSession, nil, ctxdb.Event{
		Kind: string(event.HookError), Payload: payload,
	}); err != nil {
		sessionsPrintf(g.stderr, "gateway: append hook_error: %s\n", cleanLine(err.Error()))
	}
}
