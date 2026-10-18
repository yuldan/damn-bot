package retry

import (
	"errors"
	"sync"
	"time"
)

// Circuit parameters (SPEC §4.1): 5 retryable failures within 60 s open the
// circuit for 30 s, then one half-open probe decides.
const (
	BreakerFailures = 5
	BreakerWindow   = 60 * time.Second
	BreakerOpen     = 30 * time.Second
)

// ErrCircuitOpen is returned by [Breaker.Allow] while the circuit is open or
// its single half-open probe is in flight. [ClassifyErr] maps it to
// overloaded, so model fallback may follow, but [Do] never retries it on the
// same model.
var ErrCircuitOpen = errors.New("circuit open")

// State is the circuit state shown in the status panel.
type State uint8

// Circuit states.
const (
	Closed State = iota
	Open
	HalfOpen
)

// Breaker is an in-process circuit breaker for one (provider, credential)
// route; safe for concurrent use. Sharing across processes is GAP-COST-06
// (F4). A single failure never opens it (SPEC §4.1).
type Breaker struct {
	mu        sync.Mutex
	fails     [BreakerFailures]time.Time // ring of recent retryable failures
	next      int
	state     State
	openUntil time.Time
	probing   bool
}

// Allow reports whether a request may be sent now. In half-open state only
// one caller gets nil until it calls [Breaker.Record].
func (b *Breaker) Allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == Open && !time.Now().Before(b.openUntil) {
		b.state, b.probing = HalfOpen, false
	}
	switch {
	case b.state == Open, b.state == HalfOpen && b.probing:
		return ErrCircuitOpen
	case b.state == HalfOpen:
		b.probing = true
	}
	return nil
}

// Record feeds the class of a finished request. Retryable classes count as
// failures; success closes a half-open circuit; other classes (auth,
// bad_request, …) say nothing about provider health and only end a probe
// as a success.
func (b *Breaker) Record(c Class) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if !c.Retryable() {
		if b.state == HalfOpen {
			b.state, b.probing, b.fails = Closed, false, [BreakerFailures]time.Time{}
		}
		return
	}
	if b.state == HalfOpen {
		b.trip(now)
		return
	}
	b.fails[b.next] = now
	b.next = (b.next + 1) % BreakerFailures
	// The oldest slot now holds the 5th most recent failure.
	if oldest := b.fails[b.next]; !oldest.IsZero() && now.Sub(oldest) <= BreakerWindow {
		b.trip(now)
	}
}

func (b *Breaker) trip(now time.Time) {
	b.state, b.probing, b.openUntil = Open, false, now.Add(BreakerOpen)
	b.fails = [BreakerFailures]time.Time{}
}

// State returns the current state (Open turns HalfOpen only on Allow).
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}
