//go:build perf

package codetools

import (
	"testing"
	"time"
)

// TestMeasureDurableRetryMeasuresSet2 is the integration check for the CT-09
// retry (ADR-127): when the first sample set is red, the retry must measure a
// second set. Before sampling used a b.N loop, sample() drove iterations with
// b.Loop, whose loop state is not reset by ResetTimer: the second sample set
// died with "B.Loop called with timer stopped", so no red run ever produced a
// set-2 verdict. Runs in make perf (build tag perf, -run names this test).
func TestMeasureDurableRetryMeasuresSet2(t *testing.T) {
	setBenchtime(t, "100x")
	r := testing.Benchmark(func(b *testing.B) {
		if b.N < codetoolsBudgetMinRuns {
			return // calibration pass; the framework then runs b.N = 100
		}
		sets := durableSets(true, 0, func(label string) durableSet {
			return durableMeasure(b, "retry", label, func() (func(int), func()) {
				return func(int) { spin(10 * time.Microsecond) }, func() { spin(5 * time.Microsecond) }
			})
		}, nil)
		if len(sets) != 2 {
			b.Fatalf("retry measured %d sets, want 2", len(sets))
		}
	})
	if r.N < codetoolsBudgetMinRuns {
		t.Fatalf("benchmark ran N=%d, want at least %d", r.N, codetoolsBudgetMinRuns)
	}
}
