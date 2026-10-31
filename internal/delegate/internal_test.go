package delegate

import (
	"errors"
	"testing"
	"time"
)

func TestLimiterTake(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	l := &limiter{period: 15 * time.Second}
	if ok, wait := l.take("s", now); !ok || wait != 0 {
		t.Fatalf("first take = %v/%v", ok, wait)
	}
	ok, wait := l.take("s", now.Add(5*time.Second))
	if ok || wait != 10*time.Second {
		t.Fatalf("second take = %v/%v", ok, wait)
	}
	// A denied caller does not extend its own penalty.
	if ok, _ := l.take("s", now.Add(14*time.Second)); ok {
		t.Fatal("take before the window elapsed")
	}
	if ok, _ := l.take("s", now.Add(15*time.Second)); !ok {
		t.Fatal("take at the window boundary was denied")
	}
	if ok, _ := l.take("other", now.Add(15*time.Second)); !ok {
		t.Fatal("a different key inherited the first key's window")
	}
	off := &limiter{}
	if ok, _ := off.take("s", now); !ok {
		t.Fatal("zero period must not limit")
	}
	if ok, _ := off.take("", now); !ok {
		t.Fatal("empty key must not limit")
	}
}

func TestUsageAdd(t *testing.T) {
	t.Parallel()
	var u Usage
	u.Add(Usage{Input: 2, Output: 3, Calls: 1, CostMicros: 1_500_000})
	u.Add(Usage{Input: 1, CachedInput: 4, CacheCreation: 5, Output: 6, Calls: 2, CostMicros: 500_000})
	if u.Input != 3 || u.CachedInput != 4 || u.CacheCreation != 5 || u.Output != 9 || u.Calls != 3 || u.CostMicros != 2_000_000 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestConfigDefaultsAndRanges(t *testing.T) {
	t.Parallel()
	c := Config{}
	if err := c.normalize(); err != nil {
		t.Fatal(err)
	}
	if c.MaxDepth != DefaultMaxDepth || c.MaxParallel != DefaultMaxParallel || c.MaxTasks != DefaultMaxTasks ||
		c.DefaultIter != DefaultIterations || c.PerTaskShare != DefaultPerTaskShare ||
		c.MinCallPeriod != DefaultMinCallPeriod || c.WorktreeTimeout != 30*time.Second || c.Clock == nil {
		t.Fatalf("defaults = %+v", c)
	}
	bad := []Config{
		{MaxDepth: 17},
		{MaxParallel: 17},
		{MaxTasks: 33},
		{DefaultIter: 41},
		{PerTaskShare: 1.5},
		{MinCallPeriod: -time.Second},
	}
	for _, cfg := range bad {
		if err := cfg.normalize(); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("config %+v = %v, want ErrInvalidConfig", cfg, err)
		}
	}
}
