//go:build perf

package tui

import (
	"os"
	"sort"
	"testing"
	"time"
)

// TestStartupBudget is the P12 measurement of the TUI-owned startup path:
// NewApp plus the first View over the injected config snapshot, 20 runs, p95
// ≤ 100 ms. Process exec, the Go runtime, and package init are constant
// overhead outside this package and are covered by the binary startup job;
// this test fails (never skips) when the perf lane is not configured, so a
// missing gate cannot pass silently.
func TestStartupBudget(t *testing.T) {
	if os.Getenv("DAMNBOT_TIMING") != "1" {
		t.Fatalf("perf build without DAMNBOT_TIMING=1: run it through make perf/test-timing")
	}
	const runs = 20
	host := testHost(newFakeClock(), &fakeRunner{}, fakeConfig{cfg: Config{Model: "fake/model"}})
	lat := make([]time.Duration, runs)
	for i := range runs {
		start := time.Now()
		m, err := NewApp("sess-perf", host)
		if err != nil {
			t.Fatalf("NewApp: %v", err)
		}
		if m.View().Content == "" {
			t.Fatalf("first frame is empty")
		}
		lat[i] = time.Since(start)
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p95 := lat[(runs*95+99)/100-1]
	t.Logf("P12 first frame: p50=%s p95=%s (limit 100 ms, 20 runs)", lat[runs/2], p95)
	if limit := 100 * time.Millisecond; p95 > limit {
		t.Fatalf("P12 first frame p95 = %s > %s", p95, limit)
	}
}
