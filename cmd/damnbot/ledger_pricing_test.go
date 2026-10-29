package main

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/budget"
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
	reply, err := (&providerAdapter{client: client, model: ref}).Stream(ctx, &loop.Request{Model: ref}, func(loop.Delta) {})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Usage.Price == nil || reply.Usage.Price.Version != teamRouterVersion {
		t.Fatalf("adapter dropped the route price: %+v", reply.Usage.Price)
	}
	// The first stub frame carries content, so the adapter must surface the
	// provider's measured TTFT instead of dropping it on the way to the ledger.
	if reply.Usage.TTFT <= 0 {
		t.Fatalf("adapter dropped TTFT: %+v", reply.Usage)
	}
	wantTTFT := max(1, reply.Usage.TTFT.Milliseconds())
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
	if got := readTTFT(t, db); got != wantTTFT {
		t.Errorf("ttft_ms = %d, want the measured %d", got, wantTTFT)
	}
}

// TestTTFTMillis: no token is NULL (-1), and a sub-millisecond first token is
// still evidence of a response, so it records 1 ms instead of a fake zero.
func TestTTFTMillis(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   time.Duration
		want int64
	}{
		{0, -1},
		{-time.Millisecond, -1},
		{500 * time.Microsecond, 1},
		{1234 * time.Millisecond, 1234},
	}
	for _, c := range cases {
		if got := ttftMillis(c.in); got != c.want {
			t.Errorf("ttftMillis(%s) = %d, want %d", c.in, got, c.want)
		}
	}
}

// readTTFT returns ttft_ms of the single test session; -1 means NULL.
func readTTFT(t *testing.T, db *ctxdb.Store) int64 {
	t.Helper()
	var ms int64
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get("SELECT COALESCE(ttft_ms, -1) FROM usage_ledger WHERE session_id = ?", []any{"S"}, &ms)
	})
	if err != nil {
		t.Fatal(err)
	}
	return ms
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

// badUsageReply is a 2xx stream the parser rejects (negative prompt_tokens).
func badUsageReply() providertest.Reply {
	return providertest.Reply{Frames: []string{
		`{"choices":[{"index":0,"delta":{"content":"x"}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":-10,"completion_tokens":5}}`,
		"[DONE]",
	}}
}

// ledgerSummary returns every usage_ledger row of db (all sessions).
func ledgerSummary(t *testing.T, dir string) (cost, calls, unknown int64) {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	}()
	if qerr := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT COALESCE(sum(cost_usd_micros), 0), count(*),
			COALESCE(sum(cost_usd_micros IS NULL), 0) FROM usage_ledger WHERE purpose = 'turn'`, nil, &cost, &calls, &unknown)
	}); qerr != nil {
		t.Fatal(qerr)
	}
	return cost, calls, unknown
}

// TestRunBrokenStreamRecordsAttempts (SEC6-9): a rejected stream is
// retried 3×, and every attempt that got a 2xx response is recorded as an
// unknown-cost row, so a later monetary cap can deny.
func TestRunBrokenStreamRecordsAttempts(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, badUsageReply(), badUsageReply(), badUsageReply())
	dir := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := run([]string{
		"run", "-p", "hi", "--model", "openai/gpt-fake", "--base-url", srv.URL,
		"--data-dir", dir, "--config", cfg, "--output", "json",
	}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stdout %s; stderr %s", code, out.String(), errb.String())
	}
	if got := len(srv.Requests()); got != 3 {
		t.Fatalf("provider requests = %d, want 3 attempts", got)
	}
	if cost, calls, unknown := ledgerSummary(t, dir); cost != 0 || calls != 3 || unknown != 3 {
		t.Fatalf("ledger = cost %d, %d rows (%d unknown), want 3 NULL/unknown rows", cost, calls, unknown)
	}
}
