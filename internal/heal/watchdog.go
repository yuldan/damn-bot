package heal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Defaults come from SELF-HEALING §2 (adaptive sampler) and GAP-REL-17:
// `/healthz` is 200 only while the sampler beat is at most 3 s old.
const (
	DefaultInterval = time.Second
	// DefaultIdleInterval is the GAP-COST-16 adaptive tick when no operation
	// is in flight: one wake per 10 s instead of 60 per minute.
	DefaultIdleInterval = 10 * time.Second
	DefaultStaleAfter   = 3 * time.Second
	DefaultWindow       = 10 * time.Minute
	// DefaultMaxActions is the §1.4 breaker: the 5th crash in 10 minutes
	// enters SAFE MODE instead of restarting again.
	DefaultMaxActions = 5
)

// ErrNoAction rejects a watchdog without a recovery step.
var ErrNoAction = errors.New("heal: watchdog requires a recovery action")

// WatchdogOptions configures a Watchdog. Zero durations select the defaults.
type WatchdogOptions struct {
	// Interval is the sampler tick while an operation is in flight (a Beat
	// landed within the previous interval); IdleInterval is the tick while
	// idle (GAP-COST-16, SELF-HEALING §2).
	Interval     time.Duration
	IdleInterval time.Duration
	// StaleAfter is the liveness threshold shared with /healthz.
	StaleAfter time.Duration
	// Window and MaxActions form the crash-loop breaker (§1.4).
	Window     time.Duration
	MaxActions int
	// Now overrides the clock (tests); nil means time.Now.
	Now func() time.Time
	// Action is the single recovery step of one stale episode (restart the
	// owner's child subsystem/process); at most MaxActions times per Window.
	Action func(context.Context) error
	// OnSafeMode runs once when the breaker trips (wired to EnterSafeMode).
	OnSafeMode func(reason string)
}

// Watchdog performs at most one recovery action per stale episode, bounded by
// the crash-loop breaker; a fresh Beat() re-arms it and a failed action trips
// SAFE MODE. Run owns the only ticker.
type Watchdog struct {
	interval, idleInterval, staleAfter, window time.Duration
	maxActions                                 int
	now                                        func() time.Time
	action                                     func(context.Context) error
	safe                                       func(string)
	last                                       atomic.Int64 // unix nanoseconds
	mu                                         sync.Mutex
	armed, tripped                             bool
	actions                                    []time.Time
}

// NewWatchdog validates the options and starts the beat clock now, so a
// watchdog whose owner never beats becomes stale after StaleAfter.
func NewWatchdog(o WatchdogOptions) (*Watchdog, error) {
	if o.Action == nil {
		return nil, ErrNoAction
	}
	w := &Watchdog{
		interval:     or(o.Interval, DefaultInterval),
		idleInterval: or(o.IdleInterval, DefaultIdleInterval),
		staleAfter:   or(o.StaleAfter, DefaultStaleAfter),
		window:       or(o.Window, DefaultWindow),
		maxActions:   o.MaxActions,
		now:          o.Now,
		action:       o.Action,
		safe:         o.OnSafeMode,
		armed:        true,
	}
	if w.maxActions <= 0 {
		w.maxActions = DefaultMaxActions
	}
	if w.now == nil {
		w.now = time.Now
	}
	w.last.Store(w.now().UnixNano())
	return w, nil
}

// Beat records progress from the serving loop; it re-arms the recovery action.
// The timestamp and the armed flag move under the same lock, so a Check that
// is already past its fast path cannot miss this beat and fire behind it.
func (w *Watchdog) Beat() {
	w.mu.Lock()
	w.last.Store(w.now().UnixNano())
	w.armed = true
	w.mu.Unlock()
}

// Stale reports liveness exactly as `/healthz`: a beat older than StaleAfter
// is not alive (GAP-REL-17).
func (w *Watchdog) Stale() bool {
	return w.now().Sub(time.Unix(0, w.last.Load())) > w.staleAfter
}

// Disarm gives up the pending recovery action (startup completed, or the
// owner is shutting down) without clearing the breaker. It reports whether it
// won the armed→disarmed transition; Check and Disarm race for the same edge
// under mu, so exactly one of them wins (EC-TERM-010).
func (w *Watchdog) Disarm() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	won := w.armed
	w.armed = false
	return won
}

// Reset clears the breaker after `damnbot service resume` (SELF-HEALING §1.1)
// and re-arms one action.
func (w *Watchdog) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tripped, w.armed, w.actions = false, true, nil
}

// Check runs one probe and reports whether the recovery action ran.
func (w *Watchdog) Check(ctx context.Context) bool {
	now, beat := w.now(), time.Unix(0, w.last.Load())
	if now.Sub(beat) <= w.staleAfter {
		return false
	}
	w.mu.Lock()
	if w.tripped || !w.armed {
		w.mu.Unlock()
		return false
	}
	// A Beat may have landed while this Check waited for the lock; it wins
	// the armed→disarmed transition and no action fires for this episode.
	beat = time.Unix(0, w.last.Load())
	if now.Sub(beat) <= w.staleAfter {
		w.mu.Unlock()
		return false
	}
	w.armed = false
	w.actions = recent(w.actions, now.Add(-w.window))
	if len(w.actions)+1 >= w.maxActions {
		w.mu.Unlock()
		w.trip(ctx, fmt.Sprintf("crash loop: %d stale recoveries in %s", len(w.actions)+1, w.window))
		return false
	}
	w.actions = append(w.actions, now)
	w.mu.Unlock()
	behind := now.Sub(beat)
	if err := w.action(ctx); err != nil {
		if ctx.Err() != nil {
			return true // shutdown, not a crash (no spurious safe mode)
		}
		slog.ErrorContext(ctx, "watchdog recovery failed", "behind_ms", behind.Milliseconds(), "error", err)
		w.trip(ctx, "recovery failed: "+err.Error())
		return true
	}
	slog.WarnContext(ctx, "watchdog recovery action", "behind_ms", behind.Milliseconds())
	return true
}

// Run samples until ctx is done and leaves no goroutine; callers start it with
// `go wd.Run(ctx)` and cancel ctx on shutdown. The tick adapts: 1 s while an
// operation is in flight (a Beat within the last interval) and 10 s when idle
// (GAP-COST-16, SELF-HEALING §2 P22-5). A hang can only happen while an
// operation is active, so the slower idle tick does not delay detection.
func (w *Watchdog) Run(ctx context.Context) {
	interval := w.interval
	t := time.NewTimer(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		beat := time.Unix(0, w.last.Load())
		w.Check(ctx)
		if w.now().Sub(beat) < w.interval {
			interval = w.interval
		} else {
			interval = w.idleInterval
		}
		t.Reset(interval)
	}
}

// trip enters SAFE MODE once: no further action runs until Reset.
func (w *Watchdog) trip(ctx context.Context, reason string) {
	w.mu.Lock()
	already := w.tripped
	w.tripped = true
	w.mu.Unlock()
	if already {
		return
	}
	slog.ErrorContext(ctx, "safe mode", "reason", reason)
	if w.safe != nil {
		w.safe(reason)
	}
}

// recent drops actions older than the breaker window.
func recent(actions []time.Time, start time.Time) []time.Time {
	kept := actions[:0]
	for _, a := range actions {
		if !a.Before(start) {
			kept = append(kept, a)
		}
	}
	return kept
}

// or returns v when positive, def otherwise.
func or(v, def time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return def
}
