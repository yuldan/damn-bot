// Package credpool implements the SPEC §4.1 credential pool of one provider:
// several keys or tokens, round-robin selection with least-recent-failure
// preference, bounded per-credential cooldown after 401/403/429, and a typed
// credential_exhausted failure. It holds secret values but never renders,
// encodes, or logs them; callers only receive a value to place in an auth
// header.
package credpool

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Reason is the credential failure class that starts a cooldown.
type Reason string

// Credential failure reasons. Only these rotate; every other provider class
// (400, 5xx, timeout, network) belongs to the retry/fallback policy.
const (
	ReasonAuth        Reason = "auth"         // 401 or 403
	ReasonRateLimited Reason = "rate_limited" // 429
)

// Pool bounds and cooldowns.
const (
	// MaxCredentials bounds one pool so parallel rotation attempts stay bounded.
	MaxCredentials = 16
	// DefaultRateCooldown is SPEC §4.1's cooldown when the server sends no
	// Retry-After hint.
	DefaultRateCooldown = 60 * time.Second
	// MinCooldown floors a server hint so a rotation cannot hot-loop.
	MinCooldown = time.Second
	// MaxCooldown caps every cooldown (bounded backoff).
	MaxCooldown = 15 * time.Minute
	// authCooldownBase is the first 401/403 cooldown; it doubles per
	// consecutive failure of the same credential, up to MaxCooldown.
	authCooldownBase = 30 * time.Second
)

// Typed errors (E-21).
var (
	// ErrExhausted is the sentinel of a pool whose credentials all cool down.
	ErrExhausted = errors.New("credential_exhausted")
	// ErrPool rejects an invalid pool definition.
	ErrPool = errors.New("credential pool")
)

// Credential is one pool entry. The secret is unexported and only readable
// through Secret; the value never appears in String, GoString, or JSON.
type Credential struct {
	ID     string
	secret string
}

// NewCredential builds one pool entry from an already resolved secret.
func NewCredential(id, secret string) Credential {
	return Credential{ID: id, secret: secret}
}

// Secret returns the value to place in the auth header. Callers must never
// format it.
func (c Credential) Secret() string { return c.secret }

// String renders the identity only, for logs and error messages.
func (c Credential) String() string { return "credential:" + c.ID }

// GoString renders the identity only for %#v.
func (c Credential) GoString() string { return c.String() }

// MarshalJSON emits the identity only, so a struct dump cannot leak a key.
func (c Credential) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(struct {
		ID string `json:"id"`
	}{ID: c.ID})
	if err != nil {
		return nil, fmt.Errorf("credpool: encode credential: %w", err)
	}
	return b, nil
}

func (c Credential) validate() error {
	if c.ID == "" {
		return fmt.Errorf("%w: credential id is empty", ErrPool)
	}
	if c.secret == "" {
		return fmt.Errorf("%w: credential %q has an empty secret", ErrPool, c.ID)
	}
	for i := range len(c.ID) {
		ch := c.ID[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9',
			ch == '.', ch == '_', ch == '-':
		default:
			return fmt.Errorf("%w: credential id %q must be [A-Za-z0-9._-]+", ErrPool, c.ID)
		}
	}
	return nil
}

// slot is the mutable state of one credential; guarded by Pool.mu.
type slot struct {
	cred        Credential
	until       time.Time // cooldown end; zero = available
	lastUsed    time.Time
	lastFailure time.Time
	failures    int
}

// Pool is a set of credentials of one provider. It is safe for concurrent
// use. A pool is bound to one route; it never changes the endpoint.
type Pool struct {
	mu    sync.Mutex
	slots []*slot
}

// New validates creds and returns a pool. It needs 1..MaxCredentials
// non-empty, uniquely named entries.
func New(creds []Credential) (*Pool, error) {
	if len(creds) == 0 || len(creds) > MaxCredentials {
		return nil, fmt.Errorf("%w: need 1..%d credentials, got %d", ErrPool, MaxCredentials, len(creds))
	}
	p := &Pool{slots: make([]*slot, 0, len(creds))}
	seen := make(map[string]struct{}, len(creds))
	for _, c := range creds {
		if err := c.validate(); err != nil {
			return nil, err
		}
		key := strings.ToLower(c.ID)
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("%w: duplicate credential id %q", ErrPool, c.ID)
		}
		seen[key] = struct{}{}
		p.slots = append(p.slots, &slot{cred: c})
	}
	return p, nil
}

// Len is the number of credentials.
func (p *Pool) Len() int { return len(p.slots) }

// String renders the identity only, never a value.
func (p *Pool) String() string { return "credpool[" + strconv.Itoa(len(p.slots)) + "]" }

// Acquire selects the next usable credential and records the use, which makes
// the next call pick a different entry (round-robin). Available entries are
// ordered by the later of last use and last failure, so a credential that
// failed recently is tried last (least-recent-failure). It returns false when
// every credential is cooling down.
func (p *Pool) Acquire(now time.Time) (Credential, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	idx, best := -1, time.Time{}
	for i, s := range p.slots {
		if s.until.After(now) {
			continue
		}
		score := s.lastUsed
		if s.lastFailure.After(score) {
			score = s.lastFailure
		}
		if idx < 0 || score.Before(best) {
			idx, best = i, score
		}
	}
	if idx < 0 {
		return Credential{}, false
	}
	p.slots[idx].lastUsed = now
	return p.slots[idx].cred, true
}

// Success clears the cooldown and failure streak of id.
func (p *Pool) Success(id string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s := p.find(id); s != nil {
		s.until = time.Time{}
		s.failures = 0
		s.lastUsed = now
	}
}

// Fail starts the cooldown of id for reason and returns it. An unknown id or
// reason is a no-op so a caller bug cannot invent a credential. The cooldown
// is bounded: Retry-After (rate limit) or a fixed base, then exponential for
// repeated auth failures, clamped to [MinCooldown, MaxCooldown].
func (p *Pool) Fail(id string, reason Reason, retryAfter time.Duration, now time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.find(id)
	if s == nil {
		return 0
	}
	d := cooldown(reason, retryAfter, s.failures+1)
	if d == 0 {
		return 0
	}
	s.failures++
	s.lastFailure = now
	s.until = now.Add(d)
	return d
}

func (p *Pool) find(id string) *slot {
	for _, s := range p.slots {
		if s.cred.ID == id {
			return s
		}
	}
	return nil
}

// cooldown computes the bounded cooldown of one failure.
func cooldown(reason Reason, retryAfter time.Duration, failures int) time.Duration {
	var d time.Duration
	switch reason {
	case ReasonRateLimited:
		d = retryAfter
		if d <= 0 {
			d = DefaultRateCooldown
		}
	case ReasonAuth:
		d = authCooldownBase
		for range failures - 1 {
			if d >= MaxCooldown {
				break
			}
			d *= 2
		}
	default:
		return 0
	}
	if d < MinCooldown {
		return MinCooldown
	}
	if d > MaxCooldown {
		return MaxCooldown
	}
	return d
}

// Status is one credential's identity and cooldown state; it never carries a
// value.
type Status struct {
	ID        string
	Available bool
	Cooldown  time.Duration // remaining, 0 when available
	Failures  int
}

// Status returns a snapshot in configuration order.
func (p *Pool) Status(now time.Time) []Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Status, len(p.slots))
	for i, s := range p.slots {
		st := Status{ID: s.cred.ID, Failures: s.failures}
		if s.until.After(now) {
			st.Cooldown = s.until.Sub(now)
		} else {
			st.Available = true
		}
		out[i] = st
	}
	return out
}

// NextAvailable is the shortest remaining cooldown: 0 when at least one
// credential is usable now, and the smallest wait otherwise.
func (p *Pool) NextAvailable(now time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	var next time.Duration
	for _, s := range p.slots {
		if !s.until.After(now) {
			return 0
		}
		d := s.until.Sub(now)
		if next == 0 || d < next {
			next = d
		}
	}
	return next
}

// ExhaustedError reports a pool whose credentials are all cooling down.
// It wraps ErrExhausted and never names a value.
type ExhaustedError struct {
	Provider   string
	Count      int
	RetryAfter time.Duration
}

// Error renders the typed reason without any credential value.
func (e *ExhaustedError) Error() string {
	msg := "credential_exhausted"
	if e.Provider != "" {
		msg += ": provider " + e.Provider
	}
	msg += ": all " + strconv.Itoa(e.Count) + " credential(s) are cooling down"
	if e.RetryAfter > 0 {
		msg += " (next in " + e.RetryAfter.Round(time.Second).String() + ")"
	}
	return msg
}

// Unwrap exposes the sentinel for errors.Is.
func (e *ExhaustedError) Unwrap() error { return ErrExhausted }
