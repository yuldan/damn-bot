package loop

import (
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// TestF8MonotonicDeadlines: a fake wall clock that jumps ±1 hour never kills
// a live turn, because MaxWall reads monotonic elapsed time; the same cap
// still triggers when elapsed time really passes (GAP-REL-07).
func TestF8MonotonicDeadlines(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_700_000_000, 0)
	for _, jump := range []time.Duration{-time.Hour, time.Hour, 24 * time.Hour} {
		n := 0
		tr := &turn{
			cfg: Config{
				MaxWall: time.Hour,
				Now: func() time.Time {
					n++
					return base.Add(time.Duration(n) * jump)
				},
				Since: func(time.Time) time.Duration { return 2 * time.Second },
			},
			monoStart: time.Now(),
			start:     base,
		}
		if got, stopped := tr.preflight(t.Context()); stopped {
			t.Errorf("jump %v killed the turn: %+v", jump, got)
		}
	}
	tr := &turn{
		cfg:       Config{MaxWall: time.Hour, Now: func() time.Time { return base }, Since: func(time.Time) time.Duration { return time.Hour + time.Second }},
		monoStart: time.Now(),
		start:     base,
	}
	got, stopped := tr.preflight(t.Context())
	if !stopped || got.stop != event.StopCancelled || got.detail != event.DetailMaxWall {
		t.Fatalf("elapsed cap = %+v stopped=%v", got, stopped)
	}
}
