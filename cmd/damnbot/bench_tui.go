//go:build bench

package main

import (
	"os"

	"github.com/damnbot/damnbot/internal/config"
)

// benchTUIOverrides lets the F3 baseline harness point the real TUI at a
// timestamped fake provider. It is compiled only with -tags bench.
func benchTUIOverrides(o *runOptions, cfg *config.Config) {
	if v := os.Getenv("DAMNBOT_BENCH_MODEL"); v != "" {
		cfg.Provider.Model = v
	}
	o.baseURL = os.Getenv("DAMNBOT_BENCH_BASE_URL")
	o.apiKey = os.Getenv("DAMNBOT_BENCH_API_KEY")
	o.providerAttempts = 1
}
