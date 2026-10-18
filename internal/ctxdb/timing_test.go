package ctxdb

import (
	"os"
	"testing"
	"time"
)

// timingEnv enables wall-clock budget assertions (ADR-048, TS-P08). It is set
// only by `make test-timing`, which builds without -race: under -race the
// instrumented SQLite engine alone costs 175–400 ms per transaction, so a
// timing budget there measures the detector, not the code.
const timingEnv = "DAMNBOT_TIMING"

// timingEnforced reports whether wall-clock budgets are asserted. Every
// correctness check of a scenario runs regardless; only budget assertions
// consult this.
func timingEnforced(t *testing.T) bool {
	t.Helper()
	if os.Getenv(timingEnv) != "1" {
		return false
	}
	if raceEnabled {
		t.Fatalf("%s=1 requires a build without -race (make test-timing, ADR-048)", timingEnv)
	}
	return true
}

// checkBudget asserts got ≤ limit for a named wall-clock budget when timing
// is enforced; otherwise it only logs the measurement.
func checkBudget(t *testing.T, name string, got, limit time.Duration) {
	t.Helper()
	if !timingEnforced(t) {
		t.Logf("timing budget %s not enforced (ADR-048): %v / %v", name, got, limit)
		return
	}
	if got > limit {
		t.Fatalf("timing budget %s: %v > %v", name, got, limit)
	}
}
