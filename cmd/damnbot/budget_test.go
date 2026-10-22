package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/providertest"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// runBudget runs one turn with cfgTOML and returns the JSON result and the
// number of provider requests.
func runBudget(t *testing.T, cfgTOML string, wantCode int) (turnOutput, int) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfg, []byte(cfgTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := providertest.New(t, nil, providertest.Text("answered"))
	var out, errb bytes.Buffer
	code := run([]string{
		"run", "-p", "hi", "--model", "openai/gpt-fake", "--base-url", srv.URL,
		"--data-dir", t.TempDir(), "--config", cfg, "--output", "json",
	}, &out, &errb)
	var got turnOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("exit %d, json: %v\nstdout %s\nstderr %s", code, err, out.String(), errb.String())
	}
	if code != wantCode {
		t.Fatalf("exit = %d, want %d; %+v", code, wantCode, got)
	}
	return got, len(srv.Requests())
}

// TestRunBudgetGuardDenies (P27): with a daily, monthly, or provider cap
// configured the guard runs before the call; run has no registry price yet,
// so the projection is unknown and the cap fails closed (ADR-123 butir 3) on
// its typed budget stop with 0 provider requests. A cap of another provider
// does not apply.
func TestRunBudgetGuardDenies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, cfg, detail string }{
		{"daily", "[budget]\ndaily_usd = 1.0\n", "budget_daily"},
		{"monthly", "[budget]\nmonthly_usd = 50.0\n", "budget_monthly"},
		{"provider", "[budget]\nproviders = { openai = 2.0 }\n", "budget_provider"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, reqs := runBudget(t, tc.cfg, 2)
			if got.StopReason != string(event.StopMaxTokens) || got.StopDetail != tc.detail || !strings.Contains(got.Error, "cost unknown") {
				t.Fatalf("result = %+v, want max_tokens/%s", got, tc.detail)
			}
			if reqs != 0 {
				t.Fatalf("provider requests = %d after a denial, want 0", reqs)
			}
		})
	}
	t.Run("other provider cap", func(t *testing.T) {
		t.Parallel()
		got, reqs := runBudget(t, "[budget]\nproviders = { anthropic = 1.0 }\n", 0)
		if got.StopDetail != string(event.DetailCompleted) || reqs != 1 {
			t.Fatalf("result = %+v, requests = %d", got, reqs)
		}
	})
}

// TestStreamErrorBudgetClass: the provider's wrapped DeniedError maps to the
// loop's ClassBudget with the SPEC stop; other errors keep their class.
func TestStreamErrorBudgetClass(t *testing.T) {
	t.Parallel()
	denied := &provider.Error{Class: retry.BadRequest, Err: &budget.DeniedError{Decision: budget.Decision{Reason: budget.ReasonMonthly, Cap: budget.CapMonthly}}}
	if se := streamError(denied); se.Class != loop.ClassBudget || se.Detail != event.DetailBudgetMonthly {
		t.Fatalf("denied → %+v", se)
	}
	if se := streamError(errors.New("boom")); se.Class != loop.ClassProvider || se.Detail != "" {
		t.Fatalf("plain → %+v", se)
	}
	var n budgetNotes
	n.observe(&budget.Decision{Allowed: true, Warnings: []budget.CapKind{budget.CapDaily}})
	if got := n.take(); len(got) != 1 || got[0] != "daily" || len(n.take()) != 0 {
		t.Fatalf("notes = %v", got)
	}
}

// pricedTable prices a loopback route for the guard tests.
func pricedTable(t *testing.T, baseURL string) registry.PriceTable {
	t.Helper()
	table, err := registry.ParsePriceTable([]byte(fmt.Sprintf(`
version = "guard-test"
[[price]]
id = "loopback"
base_url = %q
model = "m"
kind = "operator-contracted"
source = "test-only"
from = "2026-01-01"
input = 150000
cached_input = 15000
output = 600000
`, baseURL)))
	if err != nil {
		t.Fatal(err)
	}
	return table
}

func guardDB(t *testing.T) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	return db
}

// TestRunBudgetGuardPricedRoute (PRC-3): a priced route under the cap sends
// one request; a cap below its projection is denied before the provider; an
// unpriced route fails closed as budget_unknown_cost. It also asserts the
// composition root installs a non-nil Guard.
func TestRunBudgetGuardPricedRoute(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	srv := providertest.New(t, nil, providertest.Text("ok"))
	db := guardDB(t)
	o := &runOptions{model: "openai/m", baseURL: srv.URL, apiKey: "k"}
	req := &loop.Request{Model: "openai/m", MaxTokens: 100}

	t.Run("under cap", func(t *testing.T) {
		cfg := config.Default()
		cfg.Budget.DailyUSD = 1.0
		prov, err := newRunProviderWithTable(ctx, &cfg, db, "openai/m", o, pricedTable(t, srv.URL))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := prov.Stream(ctx, req, func(loop.Delta) {}); err != nil {
			t.Fatalf("priced call under cap: %v", err)
		}
		if got := len(srv.Requests()); got != 1 {
			t.Fatalf("requests = %d, want 1", got)
		}
	})
	t.Run("cap below projection", func(t *testing.T) {
		srv := providertest.New(t, nil, providertest.Text("ok"))
		cfg := config.Default()
		cfg.Budget.DailyUSD = 0.000001
		o := &runOptions{model: "openai/m", baseURL: srv.URL, apiKey: "k"}
		prov, err := newRunProviderWithTable(ctx, &cfg, db, "openai/m", o, pricedTable(t, srv.URL))
		if err != nil {
			t.Fatal(err)
		}
		_, err = prov.Stream(ctx, req, func(loop.Delta) {})
		var se *loop.StreamError
		if !errors.As(err, &se) || se.Class != loop.ClassBudget || se.Detail != event.DetailBudgetDaily {
			t.Fatalf("priced denial = %v, want budget_daily", err)
		}
		if got := len(srv.Requests()); got != 0 {
			t.Fatalf("requests = %d, want 0", got)
		}
	})
	t.Run("unpriced fails closed", func(t *testing.T) {
		srv := providertest.New(t, nil, providertest.Text("ok"))
		cfg := config.Default()
		cfg.Budget.DailyUSD = 1.0
		o := &runOptions{model: "openai/m", baseURL: srv.URL, apiKey: "k"}
		prov, err := newRunProvider(ctx, &cfg, db, "openai/m", o)
		if err != nil {
			t.Fatal(err)
		}
		_, err = prov.Stream(ctx, req, func(loop.Delta) {})
		var se *loop.StreamError
		if !errors.As(err, &se) || se.Class != loop.ClassBudget || !strings.Contains(err.Error(), "cost unknown") {
			t.Fatalf("unpriced denial = %v, want budget_unknown_cost", err)
		}
		if got := len(srv.Requests()); got != 0 {
			t.Fatalf("requests = %d, want 0", got)
		}
	})
}

// TestRunBrokenStreamStopsUnderCap (PRC-3): after one paid attempt on a
// priced route, the retry projection exceeds the cap, so the real guard
// denies the retry with no second provider request.
func TestRunBrokenStreamStopsUnderCap(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	srv := providertest.New(t, nil, badUsageReply())
	db := guardDB(t)
	cfg := config.Default()
	cfg.Budget.DailyUSD = 0.0001 // between the 1x and 2x projection of this route
	o := &runOptions{model: "openai/m", baseURL: srv.URL, apiKey: "k"}
	prov, err := newRunProviderWithTable(t.Context(), &cfg, db, "openai/m", o, pricedTable(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, err = prov.Stream(ctx, &loop.Request{Model: "openai/m", MaxTokens: 100}, func(loop.Delta) {})
	var se *loop.StreamError
	if !errors.As(err, &se) || se.Class != loop.ClassBudget || se.Detail != event.DetailBudgetDaily {
		t.Fatalf("stream = %v, want budget_daily", err)
	}
	if got := len(srv.Requests()); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}
