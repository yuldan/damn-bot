package provider_test

import (
	"errors"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/providertest"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// TestRoutePricesTeamRouter: the registry entry is selected by the actual
// route and the ledger charge (micro-USD, cached tokens included) is exact:
// 800 × 0.15 + 200 × 0.015 + 500 × 0.60 = 120 + 3 + 300 = 423 µUSD.
func TestRoutePricesTeamRouter(t *testing.T) {
	t.Parallel()
	table, err := registry.DefaultPriceTable()
	if err != nil {
		t.Fatalf("default price table: %v", err)
	}
	prices, err := provider.RoutePrices(table, "https://api.teamorouter.com/v1", "gpt-5.4-mini")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	price := prices.At(at)
	if price == nil {
		t.Fatal("no price at the owner-decision date")
	}
	usage := budget.Usage{Input: 1_000, CachedInput: 200, Output: 500}
	micros, src, err := budget.Cost(usage, price)
	if err != nil || micros == nil || *micros != 423 || src != budget.CostPricingTable {
		t.Errorf("cost = %v %s %v, want 423 µUSD pricing_table", micros, src, err)
	}
	var e budget.Entry
	if aerr := e.Account(usage, price, 1_000); aerr != nil {
		t.Fatal(aerr)
	}
	if e.PricingVersion != "providers-2026-10-21/teamorouter-gpt-5.4-mini" || *e.CostMicros != 423 {
		t.Errorf("ledger entry version %q cost %d, want the registry version and 423 µUSD", e.PricingVersion, *e.CostMicros)
	}
	// Rounding is always up: one input token at 0.15 µUSD/MTok still costs 1.
	micros, _, err = budget.Cost(budget.Usage{Input: 1}, price)
	if err != nil || micros == nil || *micros != 1 {
		t.Errorf("single-token cost = %v, %v; want 1 µUSD (round up)", micros, err)
	}
}

// TestRoutePricesResellerRouteFailsClosed: the same model behind another
// base_url is unpriced; with a monetary cap the guard denies with
// budget_unknown_cost before the request reaches the network.
func TestRoutePricesResellerRouteFailsClosed(t *testing.T) {
	t.Parallel()
	table, err := registry.DefaultPriceTable()
	if err != nil {
		t.Fatalf("default price table: %v", err)
	}
	srv := providertest.New(t, nil, providertest.Text("should not run"))
	prices, err := provider.RoutePrices(table, srv.URL, "gpt-5.4-mini")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	if price := prices.At(at); price != nil {
		t.Fatalf("reseller route priced %+v", *price)
	}
	now := func() time.Time { return at }
	guard, err := budget.NewGuard(budget.GuardConfig{DailyUSD: 1}, zeroLedger{}, now)
	if err != nil {
		t.Fatal(err)
	}
	rt := provider.Route{Provider: "fake", Model: "gpt-5.4-mini", BaseURL: srv.URL, Prices: prices}
	o := client(t, &provider.Config{Routes: []provider.Route{rt}, Guard: guard, Now: now})
	_, err = o.Do(t.Context(), provider.PurposeTurn, ask("fake/gpt-5.4-mini"), nil)
	var denied *budget.DeniedError
	if !errors.As(err, &denied) || denied.Reason != budget.ReasonCostUnknown {
		t.Errorf("error = %v, want *budget.DeniedError budget_unknown_cost", err)
	}
	if got := srv.Requests(); len(got) != 0 {
		t.Errorf("provider received %d requests after denial", len(got))
	}
}
