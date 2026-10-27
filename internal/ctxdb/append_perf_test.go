//go:build perf

package ctxdb

import (
	"context"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/timing"
)

// appendP99Limit is the normative target of SPEC §11: `Store.Append` p99
// ≤ 10 ms Linux/Windows and ≤ 30 ms macOS with synchronous=FULL and the
// 3-blob-reference case of ADR-039.
func appendP99Limit() time.Duration {
	if runtime.GOOS == "darwin" {
		return 30 * time.Millisecond
	}
	return 10 * time.Millisecond
}

// TestAppendP99 enforces the append budget instead of only reporting it.
// Each measurement is 300 sequential appends (3 blob refs each) on one fresh
// store; the gate compares the **median of three independent sets** with the
// SPEC §11 threshold. A single I/O stall can move one set's p99 far past the
// budget on a shared runner while the same code measures 1–2 ms in the
// benchmark job, so one noisy set must not decide the gate; a consistent
// regression moves all three sets and the median stays red. This is not
// best-of: every set is logged, and no retry swallows a failure.
func TestAppendP99(t *testing.T) {
	const (
		sets    = 3
		samples = 300
	)
	s := benchStore(t, "run")
	ctx := context.Background()
	blob, refA, refB := benchSession(t, s, "S")
	ev := benchPayload(blob, refA, refB)
	p99s := make([]time.Duration, sets)
	for set := range sets {
		p99s[set] = measureAppendP99(t, s, ctx, ev, set, samples)
	}
	median := slices.Clone(p99s)
	slices.Sort(median)
	limit := appendP99Limit()
	t.Logf("append p99 sets: run1=%s run2=%s run3=%s median=%s (SPEC §11 limit %s, asserted limit %s; synchronous=FULL, 3 blob refs)",
		p99s[0], p99s[1], p99s[2], median[1], limit, timing.Limit(limit))
	timing.Check(t, "Store.Append p99 median of 3 (SPEC §11)", median[1], limit)
}

// measureAppendP99 runs one set of sequential appends and returns its p99.
// The event content changes per sample so SQLite cannot reuse a cached page.
func measureAppendP99(t *testing.T, s *Store, ctx context.Context, ev Event, set, samples int) time.Duration {
	t.Helper()
	ds := make([]time.Duration, 0, samples)
	for i := range samples {
		ev.Content = "append p99 set " + strconv.Itoa(set+1) + " sample " + strconv.Itoa(i)
		start := time.Now()
		if _, err := s.Append(ctx, "S", nil, ev); err != nil {
			t.Fatalf("set %d append %d: %v", set+1, i, err)
		}
		ds = append(ds, time.Since(start))
	}
	p50, p95, p99 := percentiles(ds)
	t.Logf("append set %d: %d samples p50=%s p95=%s p99=%s", set+1, samples, p50, p95, p99)
	return p99
}
