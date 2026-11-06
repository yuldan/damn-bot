package gwplat

import (
	"context"
	"fmt"
	"time"
)

// Backoff is the reconnect schedule of adapters that hold a long-lived
// connection. It is hand-rolled instead of time.Sleep so a cancelled context
// stops the wait immediately (GAP-GW-03 forbids inline sleeps).
type Backoff struct {
	Base time.Duration
	Max  time.Duration
}

// Default reconnect bounds: 1 s doubling to 1 min.
const (
	DefaultBackoffBase = time.Second
	DefaultBackoffMax  = time.Minute
)

// Delay returns the wait before reconnect attempt n (1-based).
func (b Backoff) Delay(attempt int) time.Duration {
	base := b.Base
	if base <= 0 {
		base = DefaultBackoffBase
	}
	limit := b.Max
	if limit <= 0 {
		limit = DefaultBackoffMax
	}
	if attempt < 1 {
		attempt = 1
	}
	d := base
	for range attempt - 1 {
		if d >= limit/2 {
			return limit
		}
		d *= 2
	}
	if d > limit {
		return limit
	}
	return d
}

// Wait blocks until the delay of attempt elapses or ctx ends. A cancelled
// context returns its error.
func (b Backoff) Wait(ctx context.Context, attempt int) error {
	timer := time.NewTimer(b.Delay(attempt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("gwplat: reconnect wait: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
