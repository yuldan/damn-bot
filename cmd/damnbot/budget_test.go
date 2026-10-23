package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/providertest"
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
