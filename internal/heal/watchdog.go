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
	DefaultInterval   = time.Second
	DefaultStaleAfter = 3 * time.Second
	DefaultWindow     = 10 * time.Minute
	// DefaultMaxActions is the §1.4 breaker: the 5th crash in 10 minutes
	// enters SAFE MODE instead of restarting again.
	DefaultMaxActions = 5
)

// ErrNoAction rejects a watchdog without a recovery step.
var ErrNoAction = errors.New("heal: watchdog requires a recovery action")

// WatchdogOptions configures a Watchdog. Zero durations select the defaults.
type WatchdogOptions struct {
	// Interval is the sampler tick; Beat() comes from the serve/loop loop.
	Interval time.Duration
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
	interval, staleAfter, window time.Duration
	maxActions                   int
	now                          func() time.Time
	action                       func(context.Context) error
	safe                         func(string)
	last                         atomic.Int64 // unix nanoseconds
	mu                           sync.Mutex
	armed, tripped               bool
	actions                      []time.Time
}

// NewWatchdog validates the options and starts the beat clock now, so a
// watchdog whose owner never beats becomes stale after StaleAfter.
func NewWatchdog(o WatchdogOptions) (*Watchdog, error) {
	if o.Action == nil {
		return nil, ErrNoAction
	}
	w := &Watchdog{
		interval:   or(o.Interval, DefaultInterval),
		staleAfter: or(o.StaleAfter, DefaultStaleAfter),
		window:     or(o.Window, DefaultWindow),
		maxActions: o.MaxActions,
		now:        o.Now,
		action:     o.Action,
		safe:       o.OnSafeMode,
		armed:      true,
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
func (w *Watchdog) Beat() {
	w.last.Store(w.now().UnixNano())
	w.mu.Lock()
	w.armed = true
	w.mu.Unlock()
}

// Stale reports liveness exactly as `/healthz`: a beat older than StaleAfter
// is not alive (GAP-REL-17).
func (w *Watchdog) Stale() bool {
	return w.now().Sub(time.Unix(0, w.last.Load())) > w.staleAfter
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
// `go wd.Run(ctx)` and cancel ctx on shutdown.
func (w *Watchdog) Run(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Check(ctx)
		}
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
