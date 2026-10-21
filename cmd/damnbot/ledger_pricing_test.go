package main

import (
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/providertest"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

const teamRouterVersion = "providers-2026-10-21/teamorouter-gpt-5.4-mini"

// ledgerDB opens a run database with one session and one event to anchor a
// usage row.
func ledgerDB(t *testing.T, sid string) *ctxdb.Store {
	t.Helper()
	ctx := t.Context()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	})
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: sid, Origin: "headless", Cwd: "/"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Append(ctx, sid, nil, ctxdb.Event{Kind: "llm_call", Role: "system", Payload: "{}"}); err != nil {
		t.Fatal(err)
	}
	return db
}

// readLedger returns the stored charge of the single test session; cost -1
// means NULL (unknown).
func readLedger(t *testing.T, db *ctxdb.Store) (cost int64, source, version string) {
	t.Helper()
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get("SELECT COALESCE(cost_usd_micros, -1), cost_source, pricing_version FROM usage_ledger WHERE session_id = ?",
			[]any{"S"}, &cost, &source, &version)
	})
	if err != nil {
		t.Fatal(err)
	}
	return cost, source, version
}

// ledgerTotal returns the summed charge and row count of sid.
func ledgerTotal(t *testing.T, db *ctxdb.Store, sid string) (cost, calls int64) {
	t.Helper()
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get("SELECT COALESCE(SUM(cost_usd_micros), 0), COUNT(*) FROM usage_ledger WHERE session_id = ?",
			[]any{sid}, &cost, &calls)
	})
	if err != nil {
		t.Fatal(err)
	}
	return cost, calls
}

// TestRunLedgerRecordsRoutePrice (REVIEW-F2-SEC6 SEC6-1): the provider
// adapter carries the route's price entry through loop.Reply into Record,
// and the ledger row stores the priced micro-USD and pricing_version.
func TestRunLedgerRecordsRoutePrice(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db := ledgerDB(t, "S")
	srv := providertest.New(t, nil, providertest.Text("ok"))
	table, err := registry.DefaultPriceTable()
	if err != nil {
		t.Fatal(err)
	}
	prices, err := provider.RoutePrices(table, "https://api.teamorouter.com/v1", "gpt-5.4-mini")
	if err != nil {
		t.Fatal(err)
	}
	net := netx.New(netx.Config{})
	t.Cleanup(net.CloseIdleConnections)
	const ref = "teamorouter/gpt-5.4-mini"
	client, err := provider.NewOpenAI(net.Client("inference", netx.Inference), &provider.Config{
		Routes: []provider.Route{{Provider: "teamorouter", Model: "gpt-5.4-mini", BaseURL: srv.URL, APIKey: "sk-test", Prices: prices}},
	})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := (&providerAdapter{client: client, model: ref, notes: &budgetNotes{}}).Stream(ctx, &loop.Request{Model: ref}, func(loop.Delta) {})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Usage.Price == nil || reply.Usage.Price.Version != teamRouterVersion {
		t.Fatalf("adapter dropped the route price: %+v", reply.Usage.Price)
	}
	// The stub reports no cost here; the charge must come from the table.
	reply.Usage.Input, reply.Usage.CachedInput, reply.Usage.Output = 1_000, 200, 500
	reply.Usage.CostUSD = nil
	l := &ctxLedger{db: db, provider: "teamorouter", model: "gpt-5.4-mini"}
	if err := l.Record(ctx, "S", "T", 0, &reply.Usage); err != nil {
		t.Fatal(err)
	}
	cost, source, version := readLedger(t, db)
	if cost != 423 || source != "pricing_table" || version != teamRouterVersion {
		t.Errorf("ledger row = %d µUSD %s %q, want 423 pricing_table %s", cost, source, version, teamRouterVersion)
	}
}

// TestRunLedgerValidatesProviderCost (SEC6-1/2/6): a priced route charges
// max(table, provider), so a low/zero/negative/NaN provider cost can never
// reduce spend; an unpriced route keeps ADR-125 precedence, and impossible
// usage leaves an unknown-cost row instead of disappearing (SEC6-8).
func TestRunLedgerValidatesProviderCost(t *testing.T) {
	t.Parallel()
	price := &event.Price{InputPerMTok: 150_000, CachedInputPerMTok: 15_000, OutputPerMTok: 600_000, Version: teamRouterVersion}
	f := func(v float64) *float64 { return &v }
	base := event.Usage{Input: 1_000, CachedInput: 200, Output: 500, Price: price}
	cases := []struct {
		name     string
		usage    event.Usage
		wantCost int64 // -1 = NULL
		wantSrc  string
		wantErr  bool
	}{
		{"provider cost below the table is raised to it", withCost(base, f(0.0001)), 423, "pricing_table", false},
		{"provider cost above the table wins", withCost(base, f(0.001)), 1_000, "provider", false},
		{"zero provider cost cannot undercut the table", withCost(base, f(0)), 423, "pricing_table", false},
		{"negative cost ignored, table applies", withCost(base, f(-0.75)), 423, "pricing_table", false},
		{"NaN cost ignored, table applies", withCost(base, f(math.NaN())), 423, "pricing_table", false},
		{"unpriced route keeps the provider cost", withCost(event.Usage{Input: 10}, f(0.0001)), 100, "provider", false},
		{"infinite cost is rejected", withCost(base, f(math.Inf(1))), 0, "", true},
		{"negative cost without a price is unknown", withCost(event.Usage{Input: 10}, f(-0.75)), -1, "unknown", false},
		{"NaN cost without a price is unknown", withCost(event.Usage{Input: 10}, f(math.NaN())), -1, "unknown", false},
		{"cached above input is rejected", event.Usage{Input: 10, CachedInput: 50, Price: price}, 0, "", true},
		{"negative tokens are rejected", event.Usage{Input: -1, Price: price}, 0, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			db := ledgerDB(t, "S")
			l := &ctxLedger{db: db, provider: "p", model: "m"}
			err := l.Record(t.Context(), "S", "T", 0, &c.usage)
			if c.wantErr {
				if err == nil {
					t.Fatal("invalid usage reported no error")
				}
				// SEC6-8: the paid call stays visible as unknown cost, so the
				// guard's unknown-cost rule denies later calls under a cap.
				if cost, src, version := readLedger(t, db); cost != -1 || src != "unknown" || version != budget.PricingNone {
					t.Errorf("invalid usage row = %d %s %q, want NULL unknown none", cost, src, version)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cost, src, _ := readLedger(t, db); cost != c.wantCost || src != c.wantSrc {
				t.Errorf("ledger row = %d %s, want %d %s", cost, src, c.wantCost, c.wantSrc)
			}
		})
	}
}

func withCost(u event.Usage, c *float64) event.Usage {
	u.CostUSD = c
	return u
}

// usageReply is an SSE answer with 1.000 prompt and 500 completion tokens and
// no provider-reported cost, so the charge comes from the route table.
func usageReply() providertest.Reply {
	return providertest.Reply{Frames: []string{
		`{"choices":[{"index":0,"delta":{"content":"ok"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":500}}`,
		"[DONE]",
	}}
}

// TestRunBudgetGuardPricedRoute (SEC6-7): a priced route built by the
// composition root (guard + price table + adapter) is charged and allowed
// until the daily cap, then denied with no further request. 1.000/500
// tokens at $0.15/$0.60 per Mtok = 450 µUSD per call; the projection adds
// 100 × $0.60 = 60 µUSD, so a 1.000 µUSD cap allows exactly 3 calls.
func TestRunBudgetGuardPricedRoute(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	srv := providertest.New(t, nil, usageReply(), usageReply(), usageReply())
	db := ledgerDB(t, "S")
	table, err := registry.ParsePriceTable([]byte(fmt.Sprintf(`
version = "probe"
[[price]]
id = "probe-local"
base_url = %q
model = "m"
kind = "operator-contracted"
source = "test-only probe"
from = "2026-01-01"
input = 150000
cached_input = 15000
output = 600000
`, srv.URL)))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Budget.DailyUSD = 0.001
	o := &runOptions{model: "openai/m", baseURL: srv.URL, apiKey: "sk-test"}
	prov, err := newRunProviderWithTable(&cfg, db, "openai/m", o, table)
	if err != nil {
		t.Fatal(err)
	}
	ledger := &ctxLedger{db: db, provider: "openai", model: "m"}
	req := &loop.Request{Model: "openai/m", MaxTokens: 100}
	for i := 1; i <= 3; i++ {
		reply, serr := prov.Stream(ctx, req, func(loop.Delta) {})
		if serr != nil {
			t.Fatalf("call %d: %v", i, serr)
		}
		if rerr := ledger.Record(ctx, "S", "T", 0, &reply.Usage); rerr != nil {
			t.Fatal(rerr)
		}
		if cost, calls := ledgerTotal(t, db, "S"); cost != 450*int64(i) || calls != int64(i) {
			t.Fatalf("after call %d: ledger %d over %d calls, want %d", i, cost, calls, 450*i)
		}
		if _, src, _ := readLedger(t, db); src != "pricing_table" {
			t.Fatalf("after call %d: source %q, want pricing_table", i, src)
		}
	}
	_, err = prov.Stream(ctx, req, func(loop.Delta) {})
	var se *loop.StreamError
	if !errors.As(err, &se) || se.Class != loop.ClassBudget || se.Detail != event.DetailBudgetDaily {
		t.Fatalf("4th call = %v, want a budget_daily denial", err)
	}
	if got := len(srv.Requests()); got != 3 {
		t.Fatalf("provider requests = %d, want 3 (none after the denial)", got)
	}
}
