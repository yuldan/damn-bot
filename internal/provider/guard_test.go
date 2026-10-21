package provider_test

import (
	"context"
	"errors"
	"testing"

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

func pricedRoute(model, url string) provider.Route {
	return provider.Route{
		Provider: "fake", Model: model, BaseURL: url, APIKey: "sk-test",
		Price: &budget.Price{InputPerMTok: 3_000_000, OutputPerMTok: 15_000_000, Version: "test"},
	}
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
	o := client(t, &provider.Config{Routes: []provider.Route{pricedRoute("m", srv.URL)}, Guard: g})
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
		Routes: []provider.Route{pricedRoute("m", srv.URL)}, Guard: g,
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
	o := client(t, &provider.Config{Routes: []provider.Route{pricedRoute("m", srv.URL)}, Guard: g})
	_, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want %v", err, boom)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("provider called after guard error")
	}
}
