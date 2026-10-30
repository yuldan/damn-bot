package main

import (
	"context"
	"fmt"
	"strconv"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/telegram"
	"github.com/damnbot/damnbot/internal/hooks"
	"github.com/damnbot/damnbot/internal/loop/event"
)

type gatewayInboundHooks struct{ reg *hooks.Registry }

func (g *serveGateway) reject(ctx context.Context, in *telegram.Inbound, out *gateway.Outcome) {
	g.metrics.IncError()
	switch out.Reason {
	case gateway.ReasonPaused:
		g.handleRejected(ctx, in, out.Reason)
	case gateway.ReasonBudgetGatewayUser:
		g.budgetNotice(ctx, in)
	case gateway.ReasonHookDenied:
		sessionsPrintf(g.stderr, "gateway: hook %s denied update %s: %s\n",
			cleanArg(out.Hook), cleanArg(in.UpdateID), cleanLine(out.HookDetail))
	case gateway.ReasonHookError:
		g.recordHookError(ctx, out)
	case gateway.ReasonNone, gateway.ReasonUnknownIdentity, gateway.ReasonNotAllowlisted,
		gateway.ReasonWeakIdentity, gateway.ReasonUnknownRole, gateway.ReasonNoWorkspace,
		gateway.ReasonMentionRequired:
	}
}

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

func (g *serveGateway) recordHookError(ctx context.Context, out *gateway.Outcome) {
	sessionsPrintf(g.stderr, "gateway: hook %s failed: %s\n", cleanArg(out.Hook), cleanLine(out.HookDetail))
	payload := `{"hook":` + strconv.Quote(out.Hook) + `,"event":"gateway_inbound","error":` + strconv.Quote(out.HookDetail) + `}`
	if _, err := g.store.DB.Append(ctx, ctxdb.SystemSession, nil, ctxdb.Event{
		Kind: string(event.HookError), Payload: payload,
	}); err != nil {
		sessionsPrintf(g.stderr, "gateway: append hook_error: %s\n", cleanLine(err.Error()))
	}
}
