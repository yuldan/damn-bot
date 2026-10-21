package compaction

import (
	"math"
	"testing"
)

func TestCompactionTrigger(t *testing.T) {
	t.Parallel()
	checker := Default()
	cases := []struct {
		estimate, window int64
		want             Kind
		percent          int64
	}{
		{0, 1000, None, 0},
		{749, 1000, None, 74},
		{750, 1000, Compact, 75},
		{899, 1000, Compact, 89},
		{900, 1000, Overflow, 90},
		{1000, 1000, Overflow, 100},
		{2000, 1000, Overflow, 200},
		{8999, 10000, Compact, 89},
		{9000, 10000, Overflow, 90},
	}
	for _, tc := range cases {
		got := checker.Decide(tc.estimate, tc.window)
		if got.Kind != tc.want || got.Percent != tc.percent {
			t.Errorf("Decide(%d,%d) = %+v, want kind %v percent %d", tc.estimate, tc.window, got, tc.want, tc.percent)
		}
		if got.Estimate != tc.estimate || got.Window != tc.window {
			t.Errorf("trigger fields = %+v", got)
		}
	}
}

func TestCompactionTriggerUnknown(t *testing.T) {
	t.Parallel()
	c := Default()
	for _, tc := range []struct{ estimate, window int64 }{
		{-1, 1000},
		{100, 0},
		{100, -5},
	} {
		got := c.Decide(tc.estimate, tc.window)
		if got.Kind != None || got.Percent != -1 {
			t.Errorf("Decide(%d,%d) = %+v, want unknown", tc.estimate, tc.window, got)
		}
	}
	// A huge estimate is an overflow and the percent arithmetic must not wrap.
	got := c.Decide(math.MaxInt64, 1000)
	if got.Kind != Overflow || got.Percent != 100 {
		t.Errorf("max estimate = %+v", got)
	}
	// A huge window is clamped so the multiplication stays inside int64.
	got = c.Decide(1, math.MaxInt64)
	if got.Kind != None || got.Percent != 0 {
		t.Errorf("max window = %+v", got)
	}
}

func TestCompactionThresholds(t *testing.T) {
	t.Parallel()
	if _, err := New(Thresholds{CompactAt: 75, OverflowAt: 75}); err == nil {
		t.Error("compact == overflow accepted")
	}
	if _, err := New(Thresholds{CompactAt: 90, OverflowAt: 80}); err == nil {
		t.Error("compact > overflow accepted")
	}
	if _, err := New(Thresholds{CompactAt: -1}); err == nil {
		t.Error("negative compact accepted")
	}
	if _, err := New(Thresholds{OverflowAt: 101}); err == nil {
		t.Error("overflow > 100 accepted")
	}
	c, err := New(Thresholds{CompactAt: 50, OverflowAt: 60})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Decide(500, 1000); got.Kind != Compact {
		t.Errorf("custom compact = %+v", got)
	}
	if got := c.Decide(600, 1000); got.Kind != Overflow {
		t.Errorf("custom overflow = %+v", got)
	}
}
