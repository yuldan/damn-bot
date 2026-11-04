//go:build !minimal

package main

import (
	"fmt"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/modules"
	"github.com/damnbot/damnbot/internal/tools"
)

// wireModules builds the deferred catalog of the frozen module registry and
// appends its meta tools to list (ADR-119). The module host of this session
// carries the ledger store, the transcript event sink and the capability seam.
func wireModules(list []tools.Tool, cfg *config.Config, db *ctxdb.Store, sid string) (runModules, *tools.Registry, error) {
	enabled, moduleConfig := moduleGate(cfg)
	mh := newRunModuleHost(db, sid)
	mods, err := newRunModules(moduleDeps{
		Registry: modules.Default(), Host: mh,
		Enabled: enabled, Config: moduleConfig,
	})
	if err != nil {
		return runModules{}, nil, err
	}
	mods.setMedia = mh.SetMedia
	reg, err := tools.NewRegistry(append(list, mods.meta...)...)
	if err != nil {
		return runModules{}, nil, fmt.Errorf("registry: %w", err)
	}
	return mods, reg, nil
}
