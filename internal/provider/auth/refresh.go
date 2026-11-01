//go:build !minimal

package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Refresh errors are sentinels; the refresher never returns its cause because
// it may name a token.
var (
	// ErrRefreshCooldown rejects a retry after repeated failures.
	ErrRefreshCooldown = errors.New("auth: refresh cooldown")
	// ErrRefreshFailed hides the underlying token endpoint error.
	ErrRefreshFailed = errors.New("auth: refresh failed")
)

// RefreshFunc renews one credential.
type RefreshFunc func(ctx context.Context) error

// CooldownRefresher serialises refreshes: ten parallel 401s run one refresh,
// and a failed refresh blocks retries until the cooldown expires.
type CooldownRefresher struct {
	fn       RefreshFunc
	cooldown time.Duration
	now      func() time.Time
	gen      atomic.Uint64
	mu       sync.Mutex
	until    time.Time
	failures int
}

// NewCooldownRefresher builds a refresher; cooldown <= 0 defaults to 30 s.
func NewCooldownRefresher(fn RefreshFunc, cooldown time.Duration) *CooldownRefresher {
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &CooldownRefresher{fn: fn, cooldown: cooldown, now: time.Now}
}

// SetClock injects the cooldown clock (tests).
func (c *CooldownRefresher) SetClock(now func() time.Time) { c.now = now }

// Failures returns the consecutive failure count.
func (c *CooldownRefresher) Failures() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failures
}

// Epoch is the generation captured before a request attempt.
func (c *CooldownRefresher) Epoch() uint64 { return c.gen.Load() }

// Refresh implements provider.TokenRefresher.
func (c *CooldownRefresher) Refresh(ctx context.Context, since uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen.Load() != since {
		return nil // another request already renewed the credential
	}
	now := c.now()
	if now.Before(c.until) {
		return fmt.Errorf("%w: %s", ErrRefreshCooldown, c.until.Sub(now).Round(time.Second))
	}
	if err := c.fn(ctx); err != nil {
		c.failures++
		c.until = now.Add(c.cooldown)
		return ErrRefreshFailed
	}
	c.failures = 0
	c.until = time.Time{}
	c.gen.Add(1)
	return nil
}

// Refresher returns the provider seam over this OAuth transport's token store.
func (o *OAuth) Refresher(cooldown time.Duration) *CooldownRefresher {
	return NewCooldownRefresher(func(ctx context.Context) error {
		tok, err := o.cfg.Store.Load(ctx, o.cfg.Provider)
		if err != nil {
			return err
		}
		return o.Refresh(ctx, &tok)
	}, cooldown)
}
