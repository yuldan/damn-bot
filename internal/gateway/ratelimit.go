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

// Limiter is a per-chat, per-group plus global token bucket (GAP-GW-03). Take
// never blocks and never sleeps: it reports how long the caller must wait, and
// the outbox stores that in next_at, so pacing never stalls a turn.
type Limiter struct {
	mu       sync.Mutex
	perChat  Rate
	perGroup Rate
	global   Rate
	chats    map[string]*bucket
	groups   map[string]*bucket
	all      bucket
	started  bool
	until    map[string]time.Time // chat -> retry_after deadline
}

// NewLimiter builds a per-chat plus global limiter; the per-group bucket is the
// per-chat rate. Adapters that declare the §9.7 caps use NewLimiterCaps.
func NewLimiter(perChat, global Rate) (*Limiter, error) {
	return NewLimiterCaps(Caps{Rate: RateCaps{PerChat: perChat, PerGroup: perChat, Global: global}})
}

// NewLimiterCaps builds a limiter from the adapter's declared Caps. Rates are
// fixed for the process lifetime; adapters with different limits get one
// limiter per platform.
func NewLimiterCaps(caps Caps) (*Limiter, error) {
	for _, f := range [...]struct {
		rate  Rate
		label string
	}{
		{caps.Rate.PerChat, "per-chat bucket"},
		{caps.Rate.PerGroup, "per-group bucket"},
		{caps.Rate.Global, "global bucket"},
	} {
		if !f.rate.valid() {
			return nil, fmt.Errorf("%w: %s", ErrInvalidRate, f.label)
		}
	}
	return &Limiter{
		perChat:  caps.Rate.PerChat,
		perGroup: caps.Rate.PerGroup,
		global:   caps.Rate.Global,
		chats:    make(map[string]*bucket),
		groups:   make(map[string]*bucket),
		until:    make(map[string]time.Time),
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

// Take consumes one token from the chat bucket and the global bucket. It is
// the platform-less form used by unit tests; production callers pass the chat
// and its group flag through TakeChat.
func (l *Limiter) Take(chat string, at time.Time) (bool, time.Duration) {
	return l.TakeChat("", chat, false, at)
}

// TakeChat consumes one token from the (platform, chat) bucket, the
// (platform, group) bucket when group is true, and the platform-global bucket.
// ok=false means nothing was consumed and wait is the minimum time until the
// next possible send (a retry_after deadline or the slowest bucket).
func (l *Limiter) TakeChat(platform, chat string, group bool, at time.Time) (bool, time.Duration) {
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
	var g *bucket
	if group {
		key := platform + "\x00" + chat
		g = l.groups[key]
		if g == nil {
			g = &bucket{tokens: l.perGroup.Burst, last: at}
			l.groups[key] = g
		}
		g.refill(l.perGroup, at)
	}
	if b.tokens < 1 || l.all.tokens < 1 || (g != nil && g.tokens < 1) {
		return false, l.slowestLocked(b, g)
	}
	b.tokens--
	l.all.tokens--
	if g != nil {
		g.tokens--
	}
	return true, 0
}

// slowestLocked returns the longest wait of the three buckets.
func (l *Limiter) slowestLocked(chat, group *bucket) time.Duration {
	wait := chat.wait(l.perChat)
	wait = max(wait, l.all.wait(l.global))
	if group != nil {
		wait = max(wait, group.wait(l.perGroup))
	}
	return wait
}

func (l *Limiter) init(at time.Time) {
	if !l.started {
		l.all = bucket{tokens: l.global.Burst, last: at}
		l.started = true
	}
}
