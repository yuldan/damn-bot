package provider_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// fakeGuard records the pre-call consultation.
type fakeGuard struct {
	decision budget.Decision
	err      error
	calls    int
	request  budget.Request
}

func (f *fakeGuard) Check(_ context.Context, r budget.Request) (budget.Decision, error) {
	f.calls++
	f.request = r
	return f.decision, f.err
}

func pricedRoute(t *testing.T, url string) provider.Route {
	t.Helper()
	prices, err := budget.NewPrices(budget.PriceEntry{
		Price: budget.Price{InputPerMTok: 3_000_000, OutputPerMTok: 15_000_000, Version: "test"},
		From:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider.Route{Provider: "fake", Model: "m", BaseURL: url, APIKey: "sk-test", Prices: prices}
}

// TestBudgetGuardBeforeCall (P27): a denial stops the request before it
// reaches the provider, carries the typed reason, and is not retried.
func TestBudgetGuardBeforeCall(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Text("should not run"))
	g := &fakeGuard{decision: budget.Decision{
		Allowed: false, Reason: budget.ReasonDaily, Cap: budget.CapDaily,
		LimitMicros: 100, UsedMicros: 90, EstimateMicros: 20,
	}}
	o := client(t, &provider.Config{Routes: []provider.Route{pricedRoute(t, srv.URL)}, Guard: g})
	_, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	if err == nil {
		t.Fatal("denied call succeeded")
	}
	var denied *budget.DeniedError
	if !errors.As(err, &denied) || denied.Reason != budget.ReasonDaily {
		t.Errorf("error = %v, want *budget.DeniedError budget_daily", err)
	}
	if g.calls != 1 || g.request.Provider != "fake" || g.request.EstimateMicros <= 0 {
		t.Errorf("guard consulted %d times with %+v", g.calls, g.request)
	}
	if got := srv.Requests(); len(got) != 0 {
		t.Errorf("provider received %d requests after denial", len(got))
	}
}

// TestBudgetGuardWarningCallback: an allowed verdict still reports the
// 80% crossing to the caller (budget_warning), then the call proceeds.
func TestBudgetGuardWarningCallback(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Text("ok"))
	g := &fakeGuard{decision: budget.Decision{Allowed: true, Warnings: []budget.CapKind{budget.CapDaily}}}
	var seen []budget.CapKind
	o := client(t, &provider.Config{
		Routes: []provider.Route{pricedRoute(t, srv.URL)}, Guard: g,
		OnBudget: func(d budget.Decision) { seen = d.Warnings },
	})
	if _, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil); err != nil {
		t.Fatal(err)
	}
	if g.calls != 1 || len(seen) != 1 || seen[0] != budget.CapDaily {
		t.Errorf("calls %d, warnings %v", g.calls, seen)
	}
	if len(srv.Requests()) != 1 {
		t.Errorf("allowed call sent %d requests", len(srv.Requests()))
	}
}

// TestBudgetGuardErrorFailsClosed: a ledger error from the guard is a
// non-retryable failure, never a silently allowed call.
func TestBudgetGuardErrorFailsClosed(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Text("should not run"))
	boom := errors.New("ledger down")
	g := &fakeGuard{err: boom}
	o := client(t, &provider.Config{Routes: []provider.Route{pricedRoute(t, srv.URL)}, Guard: g})
	_, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want %v", err, boom)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("provider called after guard error")
	}
}

// zeroLedger is an empty usage ledger for a real budget.Guard.
type zeroLedger struct{}

func (zeroLedger) Totals(context.Context, int64, int64, string) (int64, int64, error) {
	return 0, 0, nil
}

// spyGuard records the request it forwards to a real checker.
type spyGuard struct {
	inner   budget.Checker
	request budget.Request
}

func (s *spyGuard) Check(ctx context.Context, r budget.Request) (budget.Decision, error) {
	s.request = r
	d, err := s.inner.Check(ctx, r)
	if err != nil {
		return d, fmt.Errorf("spy: %w", err)
	}
	return d, nil
}

// TestBudgetGuardTimeCorrectPrice (GAP-COST-01): the guard projection and
// Response.Price use the entry in effect at the call start; at the exact
// effective_until the successor applies, and after the last entry expires
// the projection is unknown so a monetary cap denies before any request.
func TestBudgetGuardTimeCorrectPrice(t *testing.T) {
	t.Parallel()
	switchAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	prices, err := budget.NewPrices(
		budget.PriceEntry{
			Price: budget.Price{InputPerMTok: 2_000_000, OutputPerMTok: 10_000_000, Version: "2026-08"},
			From:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Until: switchAt,
		},
		budget.PriceEntry{
			Price: budget.Price{InputPerMTok: 3_000_000, OutputPerMTok: 15_000_000, Version: "2026-09"},
			From:  switchAt, Until: end,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	// ask() estimates 1 input token; MaxTokens 1000 → output dominates.
	cases := []struct {
		name    string
		at      time.Time
		version string
		micros  int64
	}{
		{"before switch", switchAt.Add(-time.Millisecond), "2026-08", 10_002},
		{"at switch", switchAt, "2026-09", 15_003},
		{"last instant", end.Add(-time.Nanosecond), "2026-09", 15_003},
		{"expired", end, "", -1},
	}
	for _, c := range cases {
		srv := providertest.New(t, nil, providertest.Text("ok"))
		now := func() time.Time { return c.at }
		guard, err := budget.NewGuard(budget.GuardConfig{DailyUSD: 100}, zeroLedger{}, now)
		if err != nil {
			t.Fatal(err)
		}
		g := &spyGuard{inner: guard}
		rt := provider.Route{Provider: "fake", Model: "m", BaseURL: srv.URL, Prices: prices}
		o := client(t, &provider.Config{Routes: []provider.Route{rt}, Guard: g, Now: now})
		req := ask("fake/m")
		req.MaxTokens = 1000
		resp, err := o.Do(t.Context(), provider.PurposeTurn, req, nil)
		if g.request.EstimateMicros != c.micros {
			t.Errorf("%s: guard projection %d µUSD, want %d", c.name, g.request.EstimateMicros, c.micros)
		}
		if c.micros < 0 {
			var denied *budget.DeniedError
			if !errors.As(err, &denied) || denied.Reason != budget.ReasonCostUnknown || len(srv.Requests()) != 0 {
				t.Errorf("%s: err %v, %d requests; want budget_unknown_cost before any request", c.name, err, len(srv.Requests()))
			}
			continue
		}
		if err != nil || resp.Price == nil || resp.Price.Version != c.version {
			t.Errorf("%s: price %+v, err %v; want %s", c.name, resp.Price, err, c.version)
		}
	}
}
