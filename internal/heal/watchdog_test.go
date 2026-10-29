package heal

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchdogFreshBeatNoAction(t *testing.T) {
	clk := newTestClock()
	var calls atomic.Int64
	w := newWatchdog(t, clk, WatchdogOptions{Action: func(context.Context) error {
		calls.Add(1)
		return nil
	}})
	w.Beat()
	if w.Check(t.Context()) {
		t.Fatal("fresh heartbeat ran an action")
	}
	clk.Advance(2 * time.Second) // still below the 3 s /healthz threshold
	w.Beat()
	if w.Check(t.Context()) {
		t.Fatal("fresh heartbeat ran an action")
	}
	if w.Stale() {
		t.Fatal("fresh heartbeat reported stale")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("actions = %d, want 0", got)
	}
}

func TestWatchdogStaleOneAction(t *testing.T) {
	clk := newTestClock()
	var calls atomic.Int64
	w := newWatchdog(t, clk, WatchdogOptions{Action: func(context.Context) error {
		calls.Add(1)
		return nil
	}})
	clk.Advance(4 * time.Second)
	if !w.Stale() {
		t.Fatal("beat older than 3 s not stale")
	}
	if !w.Check(t.Context()) {
		t.Fatal("stale heartbeat ran no action")
	}
	// The same stale episode never runs a second action (no restart loop).
	if w.Check(t.Context()) {
		t.Fatal("stale episode ran the action twice")
	}
	clk.Advance(time.Minute)
	if w.Check(t.Context()) {
		t.Fatal("stale episode re-armed without a heartbeat")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("actions = %d, want 1", got)
	}
	// A fresh heartbeat re-arms exactly one action.
	w.Beat()
	clk.Advance(4 * time.Second)
	if !w.Check(t.Context()) {
		t.Fatal("re-armed watchdog ran no action")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("actions = %d, want 2", got)
	}
}

func TestWatchdogCrashLoopBreaker(t *testing.T) {
	clk := newTestClock()
	var calls atomic.Int64
	var mu sync.Mutex
	var safeReason string
	w := newWatchdog(t, clk, WatchdogOptions{
		Action: func(context.Context) error {
			calls.Add(1)
			return nil
		},
		OnSafeMode: func(reason string) {
			mu.Lock()
			safeReason = reason
			mu.Unlock()
		},
	})
	for i := range DefaultMaxActions - 1 {
		clk.Advance(4 * time.Second)
		if !w.Check(t.Context()) {
			t.Fatalf("stale recovery %d ran no action", i+1)
		}
		w.Beat()
	}
	clk.Advance(4 * time.Second)
	if w.Check(t.Context()) {
		t.Fatal("5th stale recovery restarted instead of entering safe mode")
	}
	if got := calls.Load(); got != DefaultMaxActions-1 {
		t.Fatalf("actions = %d, want %d", got, DefaultMaxActions-1)
	}
	mu.Lock()
	reason := safeReason
	mu.Unlock()
	if !strings.Contains(reason, "crash loop") {
		t.Fatalf("safe mode reason = %q", reason)
	}
	if w.Check(t.Context()) {
		t.Fatal("tripped watchdog ran another action")
	}
	// `service resume` resets the counter and re-arms one action.
	w.Reset()
	if !w.Check(t.Context()) {
		t.Fatal("reset watchdog ran no action")
	}
	if got := calls.Load(); got != DefaultMaxActions {
		t.Fatalf("actions after reset = %d, want %d", got, DefaultMaxActions)
	}
}

func TestWatchdogActionFailureTripsSafeMode(t *testing.T) {
	clk := newTestClock()
	var reason string
	w := newWatchdog(t, clk, WatchdogOptions{
		Action:     func(context.Context) error { return errors.New("restart failed") },
		OnSafeMode: func(r string) { reason = r },
	})
	clk.Advance(4 * time.Second)
	if !w.Check(t.Context()) {
		t.Fatal("failing action was not attempted")
	}
	if !strings.Contains(reason, "recovery failed") || !strings.Contains(reason, "restart failed") {
		t.Fatalf("safe mode reason = %q", reason)
	}
	if w.Check(t.Context()) {
		t.Fatal("watchdog kept acting after a failed recovery")
	}
}

func TestWatchdogRunStops(t *testing.T) {
	fired := make(chan struct{})
	var once sync.Once
	w, err := NewWatchdog(WatchdogOptions{
		Interval:   5 * time.Millisecond,
		StaleAfter: 5 * time.Millisecond,
		Action: func(context.Context) error {
			once.Do(func() { close(fired) })
			return nil
		},
	})
	if err != nil {
		t.Fatalf("new watchdog: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("Run never ran the recovery action")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel (goroutine leak)")
	}
}

func TestNewWatchdogRequiresAction(t *testing.T) {
	if _, err := NewWatchdog(WatchdogOptions{}); !errors.Is(err, ErrNoAction) {
		t.Fatalf("watchdog without action = %v, want ErrNoAction", err)
	}
}

// TestECTERM010WatchdogDisarmVersusFire is the "EC-TERM-010" race
// (EDGE-CASES-hermes.md): the startup transition armed→firing and the
// start-stop transition armed→disarmed share one edge. One hundred cycles
// release Check and Disarm together through a channel (never a sleep) and
// require exactly one winner per cycle, with at most that winner's single
// action.
func TestECTERM010WatchdogDisarmVersusFire(t *testing.T) {
	ctx := t.Context()
	for cycle := range 100 {
		clk := newTestClock()
		var actions atomic.Int32
		w := newWatchdog(t, clk, WatchdogOptions{Action: func(context.Context) error {
			actions.Add(1)
			return nil
		}})
		clk.Advance(4 * time.Second) // one stale episode
		gate := make(chan struct{})
		fired := make(chan bool, 1)
		disarmed := make(chan bool, 1)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-gate; fired <- w.Check(ctx) }()
		go func() { defer wg.Done(); <-gate; disarmed <- w.Disarm() }()
		close(gate)
		wg.Wait()
		checkWon, disarmWon := <-fired, <-disarmed
		if checkWon == disarmWon {
			t.Fatalf("cycle %d: Check=%v Disarm=%v, want exactly one winner", cycle, checkWon, disarmWon)
		}
		var want int32
		if checkWon {
			want = 1
		}
		if got := actions.Load(); got != want {
			t.Fatalf("cycle %d: actions = %d with Check=%v, want %d", cycle, got, checkWon, want)
		}
	}
}
