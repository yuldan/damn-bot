package gwpeer

import (
	"fmt"
	"sync"
	"time"
)

// DefaultReplayWindow is the accepted clock skew of a peer DM. A message
// outside the window is rejected even with a valid signature, which bounds the
// nonce cache.
const DefaultReplayWindow = 2 * time.Minute

// maxReplayEntries bounds the nonce and message-id caches. A peer cannot grow
// memory without bound by sending unique nonces.
const maxReplayEntries = 1 << 16

// ReplayGuard enforces nonce uniqueness and message-id idempotency. A nonce is
// reserved before dispatch; the message id is committed only after dispatch
// succeeds, so a failed dispatch can be retried with a fresh nonce.
type ReplayGuard struct {
	mu       sync.Mutex
	now      func() time.Time
	window   time.Duration
	nonces   map[string]time.Time
	messages map[string]time.Time
}

// NewReplayGuard returns a guard; nil now uses time.Now and a non-positive
// window uses DefaultReplayWindow.
func NewReplayGuard(now func() time.Time, window time.Duration) *ReplayGuard {
	if now == nil {
		now = time.Now
	}
	if window <= 0 {
		window = DefaultReplayWindow
	}
	return &ReplayGuard{
		now:      now,
		window:   window,
		nonces:   make(map[string]time.Time, 64),
		messages: make(map[string]time.Time, 64),
	}
}

// Reserve records env's nonce and reports ErrReplay for a seen nonce and
// ErrDuplicate for an already committed message id. A reserved nonce is never
// reusable, even when the following dispatch fails.
func (g *ReplayGuard) Reserve(env *Envelope) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	g.pruneLocked(now)
	if _, ok := g.nonces[env.Nonce]; ok {
		return fmt.Errorf("%w: %s", ErrReplay, env.Nonce)
	}
	if _, ok := g.messages[env.ID]; ok {
		return fmt.Errorf("%w: %s", ErrDuplicate, env.ID)
	}
	if len(g.nonces) >= maxReplayEntries {
		return fmt.Errorf("%w: nonce cache is full", ErrReplay)
	}
	g.nonces[env.Nonce] = now
	return nil
}

// Commit marks env's message id delivered, making a later retry idempotent.
func (g *ReplayGuard) Commit(env *Envelope) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.messages) >= maxReplayEntries {
		g.pruneLocked(g.now())
	}
	g.messages[env.ID] = g.now()
}

// pruneLocked drops entries older than the replay window. It requires mu.
func (g *ReplayGuard) pruneLocked(now time.Time) {
	cutoff := now.Add(-g.window)
	for k, at := range g.nonces {
		if at.Before(cutoff) {
			delete(g.nonces, k)
		}
	}
	for k, at := range g.messages {
		if at.Before(cutoff) {
			delete(g.messages, k)
		}
	}
}
