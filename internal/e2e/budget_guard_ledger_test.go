package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/ledgerstore"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// The embedded registry (internal/provider/registry/providers.toml) prices
// exactly one route. A monetary cap is provable end to end only on a priced
// route: without a price the guard cannot project the call and fails closed
// with budget_unknown_cost (ADR-123 butir 3, cmd/damnbot/budget_test.go).
const (
	pricedModel = "openai/gpt-5.4-mini"
	pricedBase  = "https://api.teamorouter.com/v1"
)

// runJSON decodes the public `run --output json` object of cmd/damnbot.
type runJSON struct {
	Schema     string `json:"schema"`
	SessionID  string `json:"session_id"`
	TurnID     string `json:"turn_id"`
	StopReason string `json:"stop_reason"`
	StopDetail string `json:"stop_detail"`
	Answer     string `json:"answer"`
	Error      string `json:"error"`
	Usage      struct {
		Input       int64    `json:"input_tokens"`
		CachedInput int64    `json:"cached_input_tokens"`
		Output      int64    `json:"output_tokens"`
		CostUSD     *float64 `json:"cost_usd"`
		Iterations  int      `json:"iterations"`
	} `json:"usage"`
}

// decodeRun decodes the child's JSON result; every raw field is logged so the
// scenario output carries the bytes that were judged.
func decodeRun(t *testing.T, what string, r Result) runJSON {
	t.Helper()
	t.Logf("%s: exit=%d\nstdout: %s\nstderr: %s", what, r.Code, strings.TrimSpace(r.Stdout), strings.TrimSpace(r.Stderr))
	var got runJSON
	if err := json.Unmarshal([]byte(r.Stdout), &got); err != nil {
		t.Fatalf("%s: stdout is not one JSON result: %v", what, err)
	}
	return got
}

// structuredRun is decodeRun for a run that may die on the transport instead
// of writing a result: it returns the zero result plus the combined output.
func structuredRun(t *testing.T, what string, r Result) (runJSON, string) {
	t.Helper()
	t.Logf("%s: exit=%d\nstdout: %s\nstderr: %s", what, r.Code, strings.TrimSpace(r.Stdout), strings.TrimSpace(r.Stderr))
	var got runJSON
	if err := json.Unmarshal([]byte(r.Stdout), &got); err != nil {
		got = runJSON{}
	}
	return got, r.Stdout + r.Stderr
}

// budgetConfig writes the [budget] section the child reads (New created an
// empty config.toml).
func budgetConfig(t *testing.T, e *Env, toml string) {
	t.Helper()
	if err := os.WriteFile(e.cfg, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
}

// withStore runs fn against the e2e data dir's store and closes it, so the
// child binary always opens a committed, unlocked database.
func withStore(t *testing.T, dir string, fn func(ctx context.Context, db *ctxdb.Store) error) {
	t.Helper()
	ctx := t.Context()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	err = fn(ctx, db)
	if cerr := db.Close(); cerr != nil {
		t.Fatalf("close store: %v", cerr)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// seedSpend appends one priced usage_ledger row of micros µUSD created at at,
// in the shape the composition root writes (ctxdb/ledgerstore.Row is
// field-identical to budget.Entry).
func seedSpend(t *testing.T, dir, provider, model string, micros int64, at time.Time) {
	t.Helper()
	withStore(t, dir, func(ctx context.Context, db *ctxdb.Store) error {
		sid := "seed-" + strconv.FormatInt(at.UnixMilli(), 10)
		if _, err := db.CreateSession(ctx, ctxdb.Session{ID: sid, Origin: "headless", Cwd: "/w", Model: model}); err != nil {
			return fmt.Errorf("create seeded session: %w", err)
		}
		ids, err := db.Append(ctx, sid, nil, ctxdb.Event{Kind: "llm_call", TurnID: "T1", Role: "assistant", Content: "seeded call"})
		if err != nil {
			return fmt.Errorf("append seeded call: %w", err)
		}
		row := ledgerstore.Row{
			SessionID: sid, TurnID: "T1", EventID: ids[0], Purpose: "turn",
			Provider: provider, Model: model, InputTokens: 1000, OutputTokens: 100,
			CostMicros:     &micros,
			CostSource:     string(budget.CostPricingTable),
			PricingVersion: "providers-2026-10-21/teamorouter-gpt-5.4-mini",
			CreatedAt:      at.UnixMilli(),
			TTFTMS:         -1,
		}
		return db.Write(ctx, func(tx *ctxdb.Tx) error {
			if _, ierr := ledgerstore.Insert(tx, &row); ierr != nil {
				return fmt.Errorf("insert ledger row: %w", ierr)
			}
			return nil
		})
	})
}

// localDay is the half-open day the guard's daily cap uses (guard.Check builds
// it from the same local clock).
func localDay(now time.Time) (from, to int64) {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return day.UnixMilli(), day.AddDate(0, 0, 1).UnixMilli()
}

// runOnRoute runs one headless turn on the registry-priced route with the
// harness temp dirs, offline (loopback only, so a call the guard lets through
// can never leave the machine).
func runOnRoute(t *testing.T, e *Env, prompt string) Result {
	t.Helper()
	args := []string{
		"run", "-p", prompt, "--model", pricedModel, "--base-url", pricedBase, "--api-key", "e2e",
		"--data-dir", e.DataDir, "--config", e.cfg, "--offline", "--output", "json",
	}
	t.Logf("exec: %s", strings.Join(args, " "))
	return e.Exec(args...)
}

// TestE2EBudgetGuardDeniesAtLedgerCap (scenario 6a of PR #239): the spend the
// usage ledger holds has reached the daily cap, so the guard refuses the next
// call before the wire — the SPEC §3.5 stop with its exit code — and the
// denial quotes the exact number the ledger row carries.
func TestE2EBudgetGuardDeniesAtLedgerCap(t *testing.T) {
	t.Parallel()
	const (
		spent = 300_000 // µUSD in the ledger
		limit = 250_000 // the cap, 0.25 USD
	)
	e := New(t)
	budgetConfig(t, e, "[budget]\ndaily_usd = 0.25\n")
	now := time.Now()
	seedSpend(t, e.DataDir, "openai", "gpt-5.4-mini", spent, now)
	t.Logf("seeded usage_ledger row: cost_usd_micros=%d created_at=%d (%s)", spent, now.UnixMilli(), now.Format(time.RFC3339))
	from, to := localDay(now)
	withStore(t, e.DataDir, func(ctx context.Context, db *ctxdb.Store) error {
		used, unknown, err := ledgerstore.WindowTotals(ctx, db, from, to, "")
		if err != nil {
			return fmt.Errorf("window totals: %w", err)
		}
		t.Logf("ledgerstore.WindowTotals(from=%d, to=%d, provider=%q) = cost_usd_micros=%d unknown_rows=%d", from, to, "", used, unknown)
		if used != spent || unknown != 0 {
			t.Errorf("ledger window = (cost %d, unknown %d), want (%d, 0)", used, unknown, spent)
		}
		return nil
	})

	got := decodeRun(t, "at cap", runOnRoute(t, e, "hi"))
	if got.StopReason != string(event.StopMaxTokens) || got.StopDetail != string(event.DetailBudgetDaily) {
		t.Fatalf("stop = %s/%s, want %s/%s", got.StopReason, got.StopDetail, event.StopMaxTokens, event.DetailBudgetDaily)
	}
	if !strings.Contains(got.Error, "daily cap reached (300000 of 250000") {
		t.Errorf("denial = %q, want the ledger's 300000 of the cap's 250000 µUSD", got.Error)
	}
	if strings.Contains(got.Error, "offline") {
		t.Errorf("denial = %q: the transport was reached, so the guard did not deny", got.Error)
	}
	if n := len(e.Requests()); n != 0 {
		t.Errorf("provider requests = %d, want 0", n)
	}
}

// TestE2EBudgetGuardAllowsUnderCap is the load-bearing control of the test
// above: the same route, cap and command, with a ledger spend BELOW the cap,
// is not denied — the guard lets the call through and the request then dies on
// the offline dial rule (a non-loopback dial). Only the ledger crossing the
// cap produces the budget stop.
func TestE2EBudgetGuardAllowsUnderCap(t *testing.T) {
	t.Parallel()
	e := New(t)
	budgetConfig(t, e, "[budget]\ndaily_usd = 0.25\n")
	seedSpend(t, e.DataDir, "openai", "gpt-5.4-mini", 1_000, time.Now())
	got, out := structuredRun(t, "under cap", runOnRoute(t, e, "hi"))
	if got.StopDetail == string(event.DetailBudgetDaily) || strings.Contains(out, "cap reached") {
		t.Fatalf("call denied below the cap: stop=%s/%s output=%s", got.StopReason, got.StopDetail, out)
	}
	if !strings.Contains(strings.ToLower(out), "offline") {
		t.Fatalf("call neither denied nor attempted: %s", out)
	}
}

// TestE2EBudgetGuardIgnoresSpendOutsideThePeriod: the same spend one day
// earlier does not deny, because the daily cap sums only the current local
// day — the ledger row is load-bearing exactly inside the period.
func TestE2EBudgetGuardIgnoresSpendOutsideThePeriod(t *testing.T) {
	t.Parallel()
	e := New(t)
	budgetConfig(t, e, "[budget]\ndaily_usd = 0.25\n")
	seedSpend(t, e.DataDir, "openai", "gpt-5.4-mini", 300_000, time.Now().AddDate(0, 0, -1))
	got, out := structuredRun(t, "yesterday", runOnRoute(t, e, "hi"))
	if got.StopDetail == string(event.DetailBudgetDaily) || strings.Contains(out, "cap reached") {
		t.Fatalf("yesterday's spend denied today's call: stop=%s/%s output=%s", got.StopReason, got.StopDetail, out)
	}
}

// TestE2EBudgetLedgerRecordsExactUsage (scenario 6b of PR #239): with no
// applicable cap the turn completes on the wire and the ledger records the
// provider's own usage — the raw token counts, and a NULL cost rather than an
// invented one (the fake route is unpriced, ADR-125).
func TestE2EBudgetLedgerRecordsExactUsage(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("recorded"))
	budgetConfig(t, e, "[budget]\nproviders = { anthropic = 1.0 }\n")
	got := decodeRun(t, "under cap", e.Run("count the usage"))
	if got.StopDetail != string(event.DetailCompleted) {
		t.Fatalf("stop = %s/%s, want %s", got.StopReason, got.StopDetail, event.DetailCompleted)
	}
	if n := len(e.Requests()); n != 1 {
		t.Fatalf("provider requests = %d, want 1", n)
	}
	// providertest.Text reports prompt_tokens 100 / completion_tokens 10.
	if got.Usage.Input != 100 || got.Usage.Output != 10 {
		t.Fatalf("cli usage = %d/%d, want the provider's 100/10", got.Usage.Input, got.Usage.Output)
	}
	withStore(t, e.DataDir, func(ctx context.Context, db *ctxdb.Store) error {
		totals, err := ledgerstore.SessionTotals(ctx, db, got.SessionID)
		if err != nil {
			return fmt.Errorf("session totals: %w", err)
		}
		t.Logf("ledgerstore.SessionTotals(%s) = %+v", got.SessionID, totals)
		if totals.Calls != 1 || totals.Input != 100 || totals.Output != 10 {
			t.Errorf("ledger = %+v, want one call of 100/10", totals)
		}
		if totals.CostMicros != 0 || totals.UnknownCost != 1 {
			t.Errorf("ledger cost = (%d micros, %d unknown), want (0, 1): an unpriced call must not be charged", totals.CostMicros, totals.UnknownCost)
		}
		var source string
		var nullCost bool
		if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
			return tx.Get(`SELECT cost_source, cost_usd_micros IS NULL
				FROM usage_ledger WHERE session_id = ?1`, []any{got.SessionID}, &source, &nullCost)
		}); err != nil {
			return fmt.Errorf("read ledger row: %w", err)
		}
		t.Logf("ledger row: cost_source=%s cost_usd_micros IS NULL=%t", source, nullCost)
		if source != string(budget.CostUnknown) || !nullCost {
			t.Errorf("cost_source=%s null=%t, want unknown/true", source, nullCost)
		}
		return nil
	})
}
