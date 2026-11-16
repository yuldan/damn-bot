//go:build !minimal

package main

import (
	"context"
	"fmt"

	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/ctxdb/ledgerstore"
	"github.com/damnbot/damnbot/internal/gateway"
)

// SessionStatus implements gateway.StatusDetail: the caller's own session,
// model, ledger tokens, and last prompt size (SLASH-038). It reads the same
// bindings /usage uses, so both commands agree on the session.
func (g *serveGateway) SessionStatus(ctx context.Context, scope *gateway.Scope) (gateway.SessionStatus, error) {
	route := gwstore.Route{Platform: scope.Platform, ChatID: scope.ChatID, ThreadID: scope.ThreadID}
	sid, found, err := g.store.Session(ctx, route, scope.Label())
	if err != nil {
		return gateway.SessionStatus{}, fmt.Errorf("gateway: status session: %w", err)
	}
	if !found {
		return gateway.SessionStatus{}, nil
	}
	info, err := g.store.DB.SessionInfo(ctx, sid)
	if err != nil {
		return gateway.SessionStatus{}, fmt.Errorf("gateway: status session info: %w", err)
	}
	totals, err := ledgerstore.SessionTotals(ctx, g.store.DB, sid)
	if err != nil {
		return gateway.SessionStatus{}, fmt.Errorf("gateway: status totals: %w", err)
	}
	var used int64
	if err := g.store.DB.Rows(ctx, `SELECT COALESCE((SELECT input_tokens FROM usage_ledger
		WHERE session_id = ?1 AND purpose = 'turn' ORDER BY id DESC LIMIT 1), 0)`,
		[]any{sid}, []any{&used}, func() error { return nil }); err != nil {
		return gateway.SessionStatus{}, fmt.Errorf("gateway: status context: %w", err)
	}
	return gateway.SessionStatus{
		SessionID: sid, Model: info.Model, InputTokens: totals.Input, OutputTokens: totals.Output,
		ContextUsed: used, ContextLimit: int64(g.cfg.Loop.TurnMaxInputTok),
	}, nil
}
