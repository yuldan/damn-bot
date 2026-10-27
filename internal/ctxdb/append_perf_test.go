//go:build perf

package ctxdb

import (
	"context"
	"runtime"
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

// TestAppendP99 enforces the append budget instead of only reporting it:
// 300 sequential appends (3 blob refs each) on a fresh store, p99 compared to
// the SPEC §11 threshold. A setup failure is fatal, never a skip, so a runner
// that cannot measure the budget fails the gate.
func TestAppendP99(t *testing.T) {
	const samples = 300
	s := benchStore(t, "run")
	ctx := context.Background()
	blob, refA, refB := benchSession(t, s, "S")
	ev := benchPayload(blob, refA, refB)
	ds := make([]time.Duration, 0, samples)
	for i := range samples {
		ev.Content = "append p99 sample " + strconv.Itoa(i)
		start := time.Now()
		if _, err := s.Append(ctx, "S", nil, ev); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		ds = append(ds, time.Since(start))
	}
	p50, p95, p99 := percentiles(ds)
	limit := appendP99Limit()
	t.Logf("append %d samples: p50=%s p95=%s p99=%s (SPEC §11 limit %s, asserted limit %s; synchronous=FULL, 3 blob refs)",
		samples, p50, p95, p99, limit, timing.Limit(limit))
	timing.Check(t, "Store.Append p99 (SPEC §11)", p99, limit)
}
