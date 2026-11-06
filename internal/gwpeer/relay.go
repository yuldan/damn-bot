package gwpeer

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"slices"
	"sync"
	"time"
)

// Relay forwards one peer DM to an allowlisted profile. It re-signs the
// forwarded envelope as the relay, keeps the original message id for
// idempotency, and refuses a profile outside the allowlist.
type Relay struct {
	LocalID string
	Key     ed25519.PrivateKey
	Allow   []string
	Forward func(ctx context.Context, env *Envelope) error
	Guard   *ReplayGuard
	Now     func() time.Time

	guardOnce sync.Once
}

// Relay validates env, increments its hop count, re-signs it for the target
// profile, and calls Forward exactly once per message id. A repeated id is
// reported as ErrDuplicate and is not forwarded again.
func (r *Relay) Relay(ctx context.Context, env *Envelope) error {
	if env.Hop >= MaxRelayHops {
		return fmt.Errorf("%w: hop %d", ErrLoop, env.Hop)
	}
	if !slices.Contains(r.Allow, env.To) {
		return fmt.Errorf("%w: %s", ErrProfileDenied, env.To)
	}
	if r.Forward == nil {
		return fmt.Errorf("%w: relay has no forwarder", ErrProfileDenied)
	}
	if err := r.guard().Reserve(env); err != nil {
		return err
	}
	out := *env
	out.Hop = env.Hop + 1
	out.From = r.LocalID
	out.Nonce = NewID()
	out.Timestamp = r.now().Unix()
	out.Signature = nil
	if err := out.Sign(r.Key); err != nil {
		return err
	}
	if err := r.Forward(ctx, &out); err != nil {
		return fmt.Errorf("gwpeer: relay %s: %w", env.ID, err)
	}
	r.guard().Commit(env)
	return nil
}

func (r *Relay) guard() *ReplayGuard {
	r.guardOnce.Do(func() {
		if r.Guard == nil {
			r.Guard = NewReplayGuard(r.Now, DefaultReplayWindow)
		}
	})
	return r.Guard
}

func (r *Relay) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
