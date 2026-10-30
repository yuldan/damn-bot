package gateway_test

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
)

// TestGoroutineBounds is the GAP-GW-02 resource bound: 1,000 messages must not
// grow the goroutine count, because turns run on the fixed
// max_parallel_turns pool rather than one goroutine per message.
func TestGoroutineBounds(t *testing.T) {
	base := runtime.NumGoroutine()
	clk := newFakeClock()
	s := quotaScheduler(t, clk, 2, gateway.QuotaOptions{
		MessagesPerMinute: 1_000_000,
		TurnsPerDay:       1_000_000,
	})
	rec := &recorder{done: make(chan struct{}, 1)}
	for i := range 1000 {
		v, err := s.Submit(context.Background(), quotaReq("u1", "dm", false), ticket("u1", "dm"), rec.run)
		if err != nil || v.Kind != gateway.Enqueued {
			t.Fatalf("message %d: %+v err=%v", i, v, err)
		}
		rec.wait(t, 1)
	}
	if got := rec.count(); got != 1000 {
		t.Fatalf("runs = %d, want 1000", got)
	}
	runtime.GC()
	after := runtime.NumGoroutine()
	// Two workers persist; nothing may grow with the message count.
	if after > base+2+2 {
		t.Fatalf("goroutines grew with messages: before=%d after1000=%d", base, after)
	}
	s.Close()
	closed := runtime.NumGoroutine()
	if closed > base+2 {
		t.Fatalf("goroutines did not return after Close: before=%d closed=%d", base, closed)
	}
	t.Logf("goroutines before=%d after1000=%d afterClose=%d (workers=2)", base, after, closed)
}

// TestSchedulerSerializesIdentity proves the second message of one identity
// never runs while the first is active, even with free global workers.
func TestSchedulerSerializesIdentity(t *testing.T) {
	clk := newFakeClock()
	s := quotaScheduler(t, clk, 4, gateway.QuotaOptions{MessagesPerMinute: 1000})
	rec := &recorder{
		done:    make(chan struct{}, 2),
		started: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	if v, err := s.Submit(context.Background(), quotaReq("u1", "dm", false), ticket("u1", "dm"), rec.run); err != nil || v.Kind != gateway.Enqueued {
		t.Fatalf("first: %+v err=%v", v, err)
	}
	if v, err := s.Submit(context.Background(), quotaReq("u1", "dm", false), ticket("u1", "dm"), rec.run); err != nil || v.Kind != gateway.Enqueued || v.Position != 2 {
		t.Fatalf("second: %+v err=%v", v, err)
	}
	<-rec.started
	select {
	case <-rec.started:
		t.Fatal("two turns of one identity ran concurrently")
	default:
	}
	close(rec.release)
	rec.wait(t, 2)
	if got := rec.peakRuns(); got != 1 {
		t.Fatalf("identity peak concurrency = %d, want 1", got)
	}
}

// TestSchedulerNewDefaults pins the SPEC defaults where config supplies none.
func TestSchedulerNewDefaults(t *testing.T) {
	q, err := gateway.NewQuota(gateway.QuotaOptions{Clock: newFakeClock()})
	if err != nil {
		t.Fatal(err)
	}
	s, err := gateway.NewScheduler(gateway.SchedulerOptions{Quota: q})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := s.Submit(context.Background(), quotaReq("u1", "dm", false), ticket("u1", "dm"), (&recorder{}).run); !errors.Is(err, gateway.ErrSchedulerClosed) {
		t.Fatalf("Submit after Close = %v", err)
	}
}

// TestSchedulerStats proves /status reads a real queue: one running turn plus
// one waiting turn is exactly (queued=1, active=1). The zero check runs after
// Close, which waits for the workers and therefore for finish(); checking right
// after the work returns would race the worker's bookkeeping.
func TestSchedulerStats(t *testing.T) {
	s := quotaScheduler(t, newFakeClock(), 2, gateway.QuotaOptions{MessagesPerMinute: 1000})
	rec := &recorder{
		done: make(chan struct{}, 2), started: make(chan struct{}, 2), release: make(chan struct{}),
	}
	if v, err := s.Submit(context.Background(), quotaReq("u1", "dm", false), ticket("u1", "dm"), rec.run); err != nil || v.Kind != gateway.Enqueued {
		t.Fatalf("first: %+v err=%v", v, err)
	}
	<-rec.started
	if queued, active := s.Stats(); queued != 0 || active != 1 {
		t.Fatalf("stats = (%d queued, %d active), want (0, 1)", queued, active)
	}
	if v, err := s.Submit(context.Background(), quotaReq("u1", "dm", false), ticket("u1", "dm"), rec.run); err != nil || v.Kind != gateway.Enqueued {
		t.Fatalf("second: %+v err=%v", v, err)
	}
	if queued, active := s.Stats(); queued != 1 || active != 1 {
		t.Fatalf("stats = (%d queued, %d active), want (1, 1)", queued, active)
	}
	close(rec.release)
	rec.wait(t, 2)
	if got := rec.count(); got != 2 {
		t.Fatalf("runs = %d, want 2", got)
	}
	// Close waits for the workers, so every finish() has run by the time it
	// returns; Stats can only be read as (0, 0) after that barrier.
	s.Close()
	if queued, active := s.Stats(); queued != 0 || active != 0 {
		t.Fatalf("stats after Close = (%d queued, %d active), want (0, 0)", queued, active)
	}
}
