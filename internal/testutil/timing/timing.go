// Package timing is the single gate for wall-clock budget assertions in tests
// and benchmarks (ADR-048, ADR-073, ADR-086 NEW-35). Every correctness check
// runs in every build; only the budget assertion consults Enforced.
package timing

import (
	"os"
	"testing"
	"time"
)

// Env enables wall-clock budget assertions. It is set only by `make
// test-timing` and `make perf`, which build without -race: under -race the
// instrumented code costs several times more, so a budget there measures the
// detector, not the code.
const Env = "DAMNBOT_TIMING"

// Enforced reports whether wall-clock budgets are asserted. Asking for
// enforcement in a -race build is a misconfigured job and fails the test.
func Enforced(tb testing.TB) bool {
	tb.Helper()
	if os.Getenv(Env) != "1" {
		return false
	}
	if RaceEnabled {
		tb.Fatalf("%s=1 requires a build without -race (make test-timing, ADR-048)", Env)
	}
	return true
}

// Check asserts got ≤ limit×Scale for a named wall-clock budget when timing
// is enforced; otherwise it only logs the measurement. Scale is 1 except in
// the `perf` build, where absolute times get the ×1.5 shared-runner margin
// (ADR-077 point 4).
func Check(tb testing.TB, name string, got, limit time.Duration) {
	tb.Helper()
	limit = limit * scaleNum / scaleDen
	if !Enforced(tb) {
		tb.Logf("timing budget %s not enforced (ADR-048): %v / %v", name, got, limit)
		return
	}
	if got > limit {
		tb.Fatalf("timing budget %s: %v > %v", name, got, limit)
	}
}
