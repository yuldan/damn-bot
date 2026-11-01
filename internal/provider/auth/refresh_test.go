//go:build !minimal

package auth

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestCooldownRefresherFailure: a failed refresh enters cooldown, the cause
// (which may contain a token) is not returned, and the clock reopens it.
func TestCooldownRefresherFailure(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	c := NewCooldownRefresher(func(context.Context) error {
		calls.Add(1)
		return errors.New("refresh-TOKEN-SECRET")
	}, time.Minute)
	c.SetClock(func() time.Time { return now })
	err := c.Refresh(context.Background(), c.Epoch())
	if !errors.Is(err, ErrRefreshFailed) || strings.Contains(err.Error(), "TOKEN-SECRET") {
		t.Fatalf("failure = %v", err)
	}
	if c.Failures() != 1 {
		t.Fatalf("failures = %d, want 1", c.Failures())
	}
	if err := c.Refresh(context.Background(), c.Epoch()); !errors.Is(err, ErrRefreshCooldown) {
		t.Fatalf("second call = %v, want cooldown", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("cooldown ran %d calls", calls.Load())
	}
	now = now.Add(2 * time.Minute)
	if err := c.Refresh(context.Background(), c.Epoch()); !errors.Is(err, ErrRefreshFailed) || calls.Load() != 2 {
		t.Fatalf("after cooldown = %v, calls=%d", err, calls.Load())
	}
}
