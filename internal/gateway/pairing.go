package gateway

import (
	"context"
	"errors"
	"time"
)

// PairingStore redeems one chat pairing code for the adapter-authenticated
// identity (SPEC §9.7, GW-003). Its only implementation is an adapter over the
// existing ctxdb.RedeemPairing in the composition root (ADR-074), so pairing has
// one implementation and the gateway imports no store. Errors must be the closed
// values below; anything else is a generic failure.
type PairingStore interface {
	RedeemPairing(ctx context.Context, platform, code, externalID string, now time.Time) error
}

// Closed pairing errors a [PairingStore] returns; compared with errors.Is.
var (
	ErrPairingInvalid = errors.New("gateway: invalid pairing code")
	ErrPairingUsed    = errors.New("gateway: pairing code already used")
	ErrPairingExpired = errors.New("gateway: pairing code expired")
	ErrPairingQuota   = errors.New("gateway: pairing quota exhausted")
	ErrPairingBlocked = errors.New("gateway: pairing identity is blocked")
)

// pairAttemptLimit and pairAttemptWindow are the dispatcher's brute-force guard (SPEC §9.7, SEC-27).
const pairAttemptLimit, pairAttemptWindow = 5, 10 * time.Minute

// pair implements SPEC §9.7 "/pair <code>": the reply names the allowlisted
// platform id, never the code, and the sixth attempt in the window is refused.
func (c *Commands) pair(ctx context.Context, t *Ticket, cmd Command) CommandResult {
	if c.pairing == nil {
		return unsupported("pair")
	}
	if len(cmd.Args) != 1 {
		return usage("pair <code>")
	}
	if !c.allowPairAttempt(t.Scope.Platform, t.Scope.ChatID) {
		return CommandResult{Handled: true, Reply: "too many pairing attempts in this chat; try again later"}
	}
	reply := "pairing failed"
	switch err := c.pairing.RedeemPairing(ctx, t.Scope.Platform, cmd.Args[0], t.Scope.UserID, c.now()); {
	case err == nil:
		reply = "paired: " + t.Scope.Platform + ":" + t.Scope.UserID + " is now allowlisted"
	case errors.Is(err, ErrPairingUsed):
		reply = "pairing code has already been used"
	case errors.Is(err, ErrPairingExpired):
		reply = "pairing code has expired"
	case errors.Is(err, ErrPairingQuota):
		reply = "pairing quota is exhausted; try again later"
	case errors.Is(err, ErrPairingBlocked):
		reply = "pairing refused: this identity is blocked"
	case errors.Is(err, ErrPairingInvalid):
		reply = "pairing code is invalid"
	}
	return CommandResult{Handled: true, Reply: reply}
}

// allowPairAttempt records one attempt of platform/chat and reports whether it
// is inside the limit; this state is the dispatcher's, not the store's.
func (c *Commands) allowPairAttempt(platform, chat string) bool {
	key := platform + "\x00" + chat
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := c.pairAttempts[key][:0]
	for _, at := range c.pairAttempts[key] {
		if now.Sub(at) < pairAttemptWindow {
			kept = append(kept, at)
		}
	}
	if len(kept) >= pairAttemptLimit {
		c.pairAttempts[key] = kept
		return false
	}
	c.pairAttempts[key] = append(kept, now)
	return true
}
