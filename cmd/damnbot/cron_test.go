package main

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/cron"
	"github.com/damnbot/damnbot/internal/ctxdb"
)

// loopClock drives the serve loop deterministically: After(0) fires once, and
// every later After reports that the loop is waiting before blocking on ticks.
type loopClock struct {
	mu      sync.Mutex
	now     time.Time
	ticks   chan time.Time
	waiting chan struct{}
}

func (c *loopClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *loopClock) After(d time.Duration) <-chan time.Time {
	if d == 0 {
		ch := make(chan time.Time, 1)
		ch <- c.Now()
		return ch
	}
	select {
	case c.waiting <- struct{}{}:
	default:
	}
	return c.ticks
}

// TestServeCronTicksDueJobOnce is AUTO-029: the loop runs a due job exactly
// once even when the same minute is observed again, and a paused job never
// runs.
func TestServeCronTicksDueJobOnce(t *testing.T) {
	t.Parallel()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close db: %v", cerr)
		}
	})
	store := newCronStore(db)
	ctx := t.Context()

	active, _, err := store.Create(ctx, &cron.Job{
		Name: "active", Schedule: "*/5 * * * *", Prompt: "p", Cwd: t.TempDir(), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Create(ctx, &cron.Job{
		Name: "paused", Schedule: "*/5 * * * *", Prompt: "p", Cwd: t.TempDir(), Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	exec := cron.ExecutorFunc(func(context.Context, *cron.Job) (string, error) {
		calls.Add(1)
		return "ok", nil
	})
	now := active.CreatedAt.Truncate(time.Minute).Add(5*time.Minute + time.Second)
	clock := &loopClock{now: now, ticks: make(chan time.Time), waiting: make(chan struct{}, 1)}
	loop := newServeCron(db, t.TempDir(), "", false, io.Discard)
	loop.clock = clock
	loop.sched.Clock = clock
	loop.sched.Executor = exec
	loop.interval = time.Minute
	loop.Start(ctx)
	defer loop.Stop()

	<-clock.waiting // the initial catch-up tick finished
	if got := calls.Load(); got != 1 {
		t.Fatalf("first tick ran %d jobs, want 1", got)
	}
	if runs := countRuns(t, db, active.ID); runs != 1 {
		t.Fatalf("cron_runs rows = %d, want 1", runs)
	}

	clock.ticks <- clock.Now() // the same minute observed again
	<-clock.waiting
	if got := calls.Load(); got != 1 {
		t.Fatalf("second tick ran %d jobs, want still 1", got)
	}
	if runs := countRuns(t, db, active.ID); runs != 1 {
		t.Fatalf("cron_runs rows = %d after the second tick, want 1", runs)
	}
}

func countRuns(t *testing.T, db *ctxdb.Store, jobID string) int {
	t.Helper()
	var n int
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT count(*) FROM cron_runs WHERE job_id = ?1`, []any{jobID}, &n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
