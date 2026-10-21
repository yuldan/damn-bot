package main

import (
	"fmt"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/limitstore"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// newRunLimits is the COST-02/PROV-02 resolver of the composition root:
// config override → embedded registry → discovery/learned in model_limits →
// fallback 32.768. It never probes the provider.
func newRunLimits(cfg *config.Config, db *ctxdb.Store, table registry.PriceTable) (*registry.Limits, error) {
	configWindows := make(map[string]int64)
	for _, p := range cfg.Providers.Custom {
		for _, m := range p.Models {
			if m.ContextLength > 0 {
				configWindows[p.Name+"/"+m.ID] = int64(m.ContextLength)
			}
		}
	}
	registryWindows := make(map[string]int64)
	for _, m := range table.Models() {
		if m.ContextWindow > 0 {
			registryWindows[m.Provider+"/"+m.Model] = m.ContextWindow
		}
	}
	limits, err := registry.NewLimits(configWindows, registryWindows, limitstore.Store{DB: db}, nil)
	if err != nil {
		return nil, fmt.Errorf("model limits: %w", err)
	}
	return limits, nil
}
