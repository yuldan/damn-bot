package gateway

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Rate is one token bucket: PerSecond refill and Burst capacity. Platform
// adapters declare their limits; Telegram is 1/s per chat with burst 1 and a
// global 30/s (SPEC §9.7).
type Rate struct {
	PerSecond float64
	Burst     float64
}

func (r Rate) valid() bool { return r.PerSecond > 0 && r.Burst >= 1 }

// ErrInvalidRate rejects a bucket with no refill or no capacity.
var ErrInvalidRate = errors.New("gateway: invalid rate")

type bucket struct {
	tokens float64
	last   time.Time
}

func (b *bucket) refill(r Rate, at time.Time) {
	if at.After(b.last) {
		b.tokens += at.Sub(b.last).Seconds() * r.PerSecond
		if b.tokens > r.Burst {
			b.tokens = r.Burst
		}
		b.last = at
	}
}

func (b *bucket) wait(r Rate) time.Duration {
	if b.tokens >= 1 {
		return 0
	}
	// Round up: a truncated duration would wake a hair before the token exists.
	return time.Duration((1-b.tokens)/r.PerSecond*float64(time.Second)) + time.Nanosecond
}

// Limiter is a per-chat plus global token bucket. Take never blocks and never
// sleeps: it reports how long the caller must wait, and the outbox stores that
// in next_at, so pacing never stalls a turn.
type Limiter struct {
	mu      sync.Mutex
	perChat Rate
	global  Rate
	chats   map[string]*bucket
	all     bucket
	started bool
	until   map[string]time.Time // chat -> retry_after deadline
}

// NewLimiter builds a limiter. Rates are fixed for the process lifetime;
// adapters with different limits get one limiter per platform.
func NewLimiter(perChat, global Rate) (*Limiter, error) {
	if !perChat.valid() {
		return nil, fmt.Errorf("%w: per-chat bucket", ErrInvalidRate)
	}
	if !global.valid() {
		return nil, fmt.Errorf("%w: global bucket", ErrInvalidRate)
	}
	return &Limiter{
		perChat: perChat,
		global:  global,
		chats:   make(map[string]*bucket),
		until:   make(map[string]time.Time),
	}, nil
}

// Penalize records a platform retry_after for chat: no send before until, even
// if the buckets have tokens. Other chats are unaffected.
func (l *Limiter) Penalize(chat string, until time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if until.After(l.until[chat]) {
		l.until[chat] = until
	}
}

// Take consumes one token from the chat bucket and the global bucket. ok=false
// means nothing was consumed and wait is the minimum time until the next
// possible send (a retry_after deadline or the slower bucket).
func (l *Limiter) Take(chat string, at time.Time) (bool, time.Duration) {
	if chat == "" {
		return false, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if until, ok := l.until[chat]; ok && until.After(at) {
		return false, until.Sub(at)
	}
	l.init(at)
	b := l.chats[chat]
	if b == nil {
		b = &bucket{tokens: l.perChat.Burst, last: at}
		l.chats[chat] = b
	}
	b.refill(l.perChat, at)
	l.all.refill(l.global, at)
	if b.tokens >= 1 && l.all.tokens >= 1 {
		b.tokens--
		l.all.tokens--
		return true, 0
	}
	wait := b.wait(l.perChat)
	if g := l.all.wait(l.global); g > wait {
		wait = g
	}
	return false, wait
}

func (l *Limiter) init(at time.Time) {
	if !l.started {
		l.all = bucket{tokens: l.global.Burst, last: at}
		l.started = true
	}
}
