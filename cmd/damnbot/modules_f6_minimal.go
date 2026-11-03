//go:build minimal

package main

import (
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/modules/host"
)

// registerModules is empty in the minimal build: no module is compiled in.
func registerModules() error { return nil }

// moduleGate in the minimal build has no registered module: every deferred
// tool stays disabled and no section is compiled in (ADR-269/ADR-310).
func moduleGate(*config.Config) (func(string) bool, func(string) host.Config) {
	return func(string) bool { return false }, func(id string) host.Config {
		return host.Config{Key: "modules." + id}
	}
}
