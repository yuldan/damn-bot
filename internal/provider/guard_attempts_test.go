package provider_test

import (
	"testing"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// boomReply is the retryable 500 every attempt test scripts.
func boomReply() providertest.Reply {
	return providertest.Reply{Status: 500, Body: `{"error":{"message":"boom"}}`}
}

// TestBudgetGuardPerAttempt (REVIEW-F2-SEC5-27): the guard is consulted
// before every attempt of a route, so a retry that could spend again is
// re-authorised against the caps; the counts must match exactly.
func TestBudgetGuardPerAttempt(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, boomReply(), boomReply(), boomReply())
	g := &fakeGuard{decision: budget.Decision{Allowed: true}}
	o := client(t, &provider.Config{Routes: []provider.Route{pricedRouteFor(t, "m", srv.URL)}, Guard: g, Attempts: 3})
	if _, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil); err == nil {
		t.Fatal("three 500s succeeded")
	}
	if got := len(srv.Requests()); got != 3 {
		t.Fatalf("provider requests = %d, want 3", got)
	}
	if g.calls != 3 {
		t.Fatalf("guard consulted %d times for 3 attempts, want 3", g.calls)
	}
}

// TestBudgetGuardPerAttemptFallback: the same holds across a fallback route
// (three primary attempts, then one fallback attempt).
func TestBudgetGuardPerAttemptFallback(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, boomReply(), boomReply(), boomReply(), providertest.Text("ok"))
	g := &fakeGuard{decision: budget.Decision{Allowed: true}}
	o := client(t, &provider.Config{
		Routes:   []provider.Route{pricedRouteFor(t, "m", srv.URL), pricedRouteFor(t, "fb", srv.URL)},
		Fallback: []string{"fake/fb"}, Guard: g, Attempts: 3,
	})
	if _, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil); err != nil {
		t.Fatal(err)
	}
	if got := len(srv.Requests()); got != 4 {
		t.Fatalf("provider requests = %d, want 4", got)
	}
	if g.calls != 4 {
		t.Fatalf("guard consulted %d times for 4 attempts, want 4", g.calls)
	}
}

// TestBudgetGuardSingleAttempt (control): without a retry the guard sees
// exactly one attempt and one request.
func TestBudgetGuardSingleAttempt(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Text("hi"))
	g := &fakeGuard{decision: budget.Decision{Allowed: true}}
	o := client(t, &provider.Config{Routes: []provider.Route{pricedRouteFor(t, "m", srv.URL)}, Guard: g, Attempts: 1})
	if _, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil); err != nil {
		t.Fatal(err)
	}
	if got := len(srv.Requests()); got != 1 || g.calls != 1 {
		t.Fatalf("requests=%d guard_calls=%d, want 1/1", got, g.calls)
	}
}

// pricedRouteFor mirrors pricedRoute with an explicit model so the fallback
// route of the projection test can carry a different ref.
func pricedRouteFor(t *testing.T, model, url string) provider.Route {
	t.Helper()
	rt := pricedRoute(t, url)
	rt.Model = model
	return rt
}
