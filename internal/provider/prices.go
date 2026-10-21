package provider

import (
	"fmt"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// RoutePrices converts the registry's dated entries for the actual route
// (base_url + model) into the schedule the guard and the ledger use. A route
// the table does not price -- for example the same model behind a different
// reseller -- yields the zero Prices, so the call is unknown-cost and a
// configured monetary cap fails closed before the request (ADR-123, ADR-128).
func RoutePrices(table registry.PriceTable, baseURL, model string) (budget.Prices, error) {
	entries := table.Lookup(baseURL, model)
	if len(entries) == 0 {
		return budget.Prices{}, nil
	}
	schedule := make([]budget.PriceEntry, len(entries))
	for i := range entries {
		e := &entries[i]
		schedule[i] = budget.PriceEntry{
			Price: budget.Price{
				InputPerMTok: e.InputPerMTok, CachedInputPerMTok: e.CachedInputPerMTok,
				OutputPerMTok: e.OutputPerMTok, Version: e.Version,
			},
			From: e.From, Until: e.Until,
		}
	}
	prices, err := budget.NewPrices(schedule...)
	if err != nil {
		return budget.Prices{}, fmt.Errorf("provider: prices for %s/%s: %w", baseURL, model, err)
	}
	return prices, nil
}
