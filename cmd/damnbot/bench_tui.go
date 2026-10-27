//go:build bench

package main

import (
	"fmt"
	"os"
	"time"

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

// benchTrace gives the F3 harness an ordering witness: with the trace env set,
// the PTY carries F3TRACE lines for config/app/frame and any lazy ensure call.
func benchTrace(phase string) {
	if os.Getenv("DAMNBOT_BENCH_TRACE") != "1" {
		return
	}
	fmt.Fprintf(os.Stderr, "F3TRACE %s %d\n", phase, time.Now().UnixNano())
}
