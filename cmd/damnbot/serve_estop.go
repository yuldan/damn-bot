package main

import (
	"context"
	"errors"
	"strings"

	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/telegram"
)

func (g *serveGateway) drainParked(ctx context.Context) bool {
	st, err := g.estop.Status(ctx)
	if err != nil {
		return false
	}
	if st.Paused {
		return true
	}
	if st.PendingSince.IsZero() {
		return false
	}
	seen := map[string]bool{}
	for ctx.Err() == nil {
		rows, err := g.store.PendingInbound(ctx, st.PendingSince.UnixMilli(), st.PendingUntil.UnixMilli(), 32)
		if err != nil {
			sessionsPrintf(g.stderr, "gateway: pending inbound: %s\n", cleanLine(err.Error()))
			g.metrics.IncError()
			return true
		}
		progress := false
		for i := range rows {
			key := rows[i].Platform + "\x1f" + rows[i].UpdateID
			if seen[key] {
				continue
			}
			seen[key], progress = true, true
			if !g.replay(ctx, &rows[i]) {
				return true
			}
		}
		if !progress {
			if derr := g.estop.Drained(ctx); derr != nil {
				sessionsPrintf(g.stderr, "gateway: clear pending window: %s\n", cleanLine(derr.Error()))
				g.metrics.IncError()
			}
			return false
		}
	}
	return ctx.Err() != nil
}

func (g *serveGateway) replay(ctx context.Context, p *gwstore.Pending) bool {
	if _, cmd := gateway.Parse(p.Text); cmd || p.Text == "" {
		return true
	}
	in := telegram.Inbound{
		Platform: p.Platform, ChatID: p.ChatID, ThreadID: p.ThreadID, SenderID: p.SenderID,
		UpdateID: p.UpdateID, Text: p.Text, Group: strings.HasPrefix(p.ChatID, "-"),
	}
	msg := gateway.Inbound{
		Platform: p.Platform, ChatID: p.ChatID, ThreadID: p.ThreadID, SenderID: p.SenderID,
		UpdateID: p.UpdateID, Group: in.Group, Mentioned: true, ReplyToBot: true, Payload: p.Payload, Text: p.Text,
	}
	out, err := g.gate.AdmitPending(ctx, &msg, func(wctx context.Context, t gateway.Ticket) error {
		turnCtx, done, ok := g.beginTurn(wctx, &t.Scope)
		if !ok {
			return errors.New("gateway: turn already active")
		}
		defer done()
		return g.run(turnCtx, &in, &t)
	})
	if err != nil {
		if ctx.Err() == nil {
			sessionsPrintf(g.stderr, "gateway: replay %s: %s\n", cleanArg(p.UpdateID), cleanLine(err.Error()))
			g.metrics.IncError()
		}
		return false
	}
	return out.Kind != gateway.Rejected || out.Reason != gateway.ReasonPaused
}

func (g *serveGateway) handleRejected(ctx context.Context, in *telegram.Inbound, reason gateway.Reason) {
	switch reason {
	case gateway.ReasonPaused:
		g.pausedNotice(ctx, in)
	case gateway.ReasonBudgetGatewayUser:
		g.metrics.IncError()
		g.budgetNotice(ctx, in)
	case gateway.ReasonNone, gateway.ReasonUnknownIdentity, gateway.ReasonNotAllowlisted,
		gateway.ReasonWeakIdentity, gateway.ReasonUnknownRole, gateway.ReasonNoWorkspace,
		gateway.ReasonMentionRequired, gateway.ReasonHookDenied, gateway.ReasonHookError:
		g.metrics.IncError()
	}
}

func (g *serveGateway) pausedNotice(ctx context.Context, in *telegram.Inbound) {
	_, err := g.adapter.Send(ctx, &telegram.Outbound{
		ChatID: in.ChatID, ThreadID: in.ThreadID, Key: gateway.DeliveryKey("telegram", in.ChatID, "paused:"+in.UpdateID),
		Text: "emergency stop is on: this message is parked until the owner runs /pause off",
	})
	if err != nil {
		g.metrics.IncError()
	}
}
