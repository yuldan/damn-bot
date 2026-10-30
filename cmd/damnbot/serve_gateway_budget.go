package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/ctxdb/ledgerstore"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/telegram"
)

// newServeGate builds admission over the shared gateway stores plus the role
// user daily USD cap (SPEC §18.4, GAP-GW-02).
func newServeGate(o *serveOptions, store gwstore.Store, users map[string]string, dailyUSD float64, db *ctxdb.Store, commands *gateway.Commands, estop *gateway.Estop) (*gateway.Gate, error) {
	budget, err := gateway.NewBudgetGuard(&gateway.BudgetOptions{
		DailyUSD: dailyUSD,
		Spend:    gatewaySpend{db: db},
	})
	if err != nil {
		return nil, fmt.Errorf("gateway budget: %w", err)
	}
	gate, err := gateway.New(&gateway.Options{
		DataDir: o.dataDir, Users: users,
		Identities: telegramStore{Store: store}, Inbox: telegramStore{Store: store},
		Commands: commands,
		Budget:   budget,
		Estop:    estop,
	})
	if err != nil {
		return nil, fmt.Errorf("gateway admission: %w", err)
	}
	return gate, nil
}

// gatewaySpend adapts ledgerstore to the gateway SpendSource (E-09, ADR-074).
type gatewaySpend struct{ db *ctxdb.Store }

func (s gatewaySpend) Spend(ctx context.Context, identity string, fromMS, toMS int64) (int64, int64, error) {
	cost, unknown, err := ledgerstore.GatewaySpend(ctx, s.db, identity, fromMS, toMS)
	if err != nil {
		return 0, 0, fmt.Errorf("gateway spend: %w", err)
	}
	return cost, unknown, nil
}

// budgetNotice sends the static role user budget reply at most once per
// minute per identity (SPEC §18.4/GAP-GW-02); the turn itself already ran zero
// LLM and zero tool calls. A send failure only increments the error metric.
func (g *serveGateway) budgetNotice(ctx context.Context, in *telegram.Inbound) {
	req := gateway.QuotaRequest{
		Platform: in.Platform, Identity: in.SenderID, ChatID: in.ChatID,
		Group: strings.HasPrefix(in.ChatID, "-"),
	}
	if !g.quota.Notice(req) {
		return
	}
	now := time.Now()
	if g.now != nil {
		now = g.now()
	}
	now = now.UTC()
	reset := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1).Format("15:04")
	if _, err := g.adapter.Send(ctx, &telegram.Outbound{
		ChatID: in.ChatID, ThreadID: in.ThreadID, Text: "limit (reset " + reset + " UTC)",
	}); err != nil {
		g.metrics.IncError()
	}
}
