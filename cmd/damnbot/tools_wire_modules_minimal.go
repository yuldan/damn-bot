//go:build minimal

package main

import (
	"fmt"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/tools"
)

// wireModules in the minimal build has no module and no deferred catalog: it
// only builds the tool registry, keeping the minimal binary free of the module
// wiring (ADR-315).
func wireModules(list []tools.Tool, _ *config.Config, _ *ctxdb.Store, _ string) (runModules, *tools.Registry, error) {
	reg, err := tools.NewRegistry(list...)
	if err != nil {
		return runModules{}, nil, fmt.Errorf("registry: %w", err)
	}
	return runModules{}, reg, nil
}

// wireRunMedia is a no-op in the minimal build: the media module is full-only.
func wireRunMedia(func(host.Completer, bool, string), loop.Provider) {}
