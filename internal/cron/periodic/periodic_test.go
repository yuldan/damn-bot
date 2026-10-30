package periodic

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

type fakeStore struct {
	mu   sync.Mutex
	rows map[string]State
}

func newStore() *fakeStore { return &fakeStore{rows: map[string]State{}} }

func (s *fakeStore) Load(_ context.Context, name string) (State, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.rows[name]
	return st, ok, nil
}

func (s *fakeStore) Save(_ context.Context, name string, st *State) error {
	s.mu.Lock()
	s.rows[name] = *st
	s.mu.Unlock()
	return nil
}

func (s *fakeStore) get(name string) State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[name]
}

type reporter struct{ ch chan Report }

func newReporter() *reporter { return &reporter{ch: make(chan Report, 16)} }

func (r *reporter) report(rep *Report) { r.ch <- *rep }

func (r *reporter) wait(t *testing.T) Report {
	t.Helper()
	select {
	case rep := <-r.ch:
		return rep
	case <-time.After(5 * time.Second):
		t.Fatal("no run report")
		return Report{}
	}
}

func base() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }

func newScheduler(t *testing.T, store Store, clock Clock, tasks ...Task) (*Scheduler, *reporter) {
	t.Helper()
	r := newReporter()
	s := &Scheduler{Store: store, Clock: clock, Report: r.report}
	for _, task := range tasks {
		if err := s.Register(task); err != nil {
			t.Fatalf("register %s: %v", task.Name, err)
		}
	}
	return s, r
}

func tick(t *testing.T, s *Scheduler) {
	t.Helper()
	if err := s.Tick(t.Context()); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

func wait(t *testing.T, s *Scheduler) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}
}

// TestFirstRun: FirstRun 0 runs on the first tick; a delayed task only stores
// its first slot.
func TestFirstRun(t *testing.T) {
	clock := &fakeClock{now: base()}
	store := newStore()
	var immediate atomic.Int32
	s, r := newScheduler(t, store, clock, Task{
		Name: "now", Interval: time.Minute, Run: func(context.Context) (string, error) {
			immediate.Add(1)
			return "", nil
		},
	})
	tick(t, s)
	if rep := r.wait(t); rep.Outcome != OutcomeOK {
		t.Fatalf("outcome = %s, want ok", rep.Outcome)
	}
	wait(t, s)
	if immediate.Load() != 1 || !store.get("now").Next.Equal(base().Add(time.Minute)) {
		t.Fatalf("immediate runs=%d next=%s", immediate.Load(), store.get("now").Next)
	}

	delayed, _ := newScheduler(t, newStore(), clock, Task{
		Name: "later", Interval: time.Minute, FirstRun: 10 * time.Minute,
		Run: func(context.Context) (string, error) { t.Error("delayed task ran"); return "", nil },
	})
	tick(t, delayed)
	wait(t, delayed)
}

// TestIntervalAndDrift: Next advances on the original cadence, so a late tick
// runs once and re-aligns instead of running once per missed slot.
func TestIntervalAndDrift(t *testing.T) {
	clock := &fakeClock{now: base()}
	var runs atomic.Int32
	store := newStore()
	s, r := newScheduler(t, store, clock, Task{
		Name: "drift", Interval: time.Minute,
		Run: func(context.Context) (string, error) { runs.Add(1); return "", nil },
	})
	tick(t, s)
	r.wait(t)
	wait(t, s)
	first := store.get("drift").Next
	if delta := first.Sub(base()); delta != time.Minute {
		t.Fatalf("next delta = %s, want the interval", delta)
	}

	clock.set(base().Add(30 * time.Second))
	tick(t, s)
	wait(t, s)
	if runs.Load() != 1 {
		t.Fatalf("runs before due = %d, want 1", runs.Load())
	}

	clock.set(base().Add(2*time.Minute + 30*time.Second)) // slot 1m missed, 2m due
	tick(t, s)
	if rep := r.wait(t); rep.Outcome != OutcomeOK || !rep.ScheduledAt.Equal(base().Add(time.Minute)) {
		t.Fatalf("report = %+v", rep)
	}
	wait(t, s)
	if runs.Load() != 2 {
		t.Fatalf("runs after gap = %d, want 2 (one replacement)", runs.Load())
	}
	if next := store.get("drift").Next; !next.Equal(base().Add(3 * time.Minute)) {
		t.Fatalf("next = %s, want cadence preserved", next)
	}
}

// TestNoOverlap: a second tick while the first run is still active starts
// nothing; the next tick after completion runs the next slot once.
func TestNoOverlap(t *testing.T) {
	clock := &fakeClock{now: base()}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var runs atomic.Int32
	s, r := newScheduler(t, newStore(), clock, Task{
		Name: "slow", Interval: time.Minute,
		Run: func(context.Context) (string, error) {
			runs.Add(1)
			started <- struct{}{}
			<-release
			return "", nil
		},
	})
	tick(t, s)
	<-started
	clock.set(base().Add(time.Minute))
	tick(t, s) // still running
	close(release)
	r.wait(t)
	wait(t, s)
	if runs.Load() != 1 {
		t.Fatalf("overlapping runs = %d, want 1", runs.Load())
	}
	clock.set(base().Add(2 * time.Minute))
	tick(t, s)
	<-started
	r.wait(t)
	wait(t, s)
	if runs.Load() != 2 {
		t.Fatalf("runs = %d, want 2", runs.Load())
	}
}

// TestBudget: a run that outlives Budget is reported as budget, not ok.
func TestBudget(t *testing.T) {
	clock := &fakeClock{now: base()}
	s, r := newScheduler(t, newStore(), clock, Task{
		Name: "slow", Interval: time.Minute, Budget: 20 * time.Millisecond,
		Run: func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	})
	tick(t, s)
	rep := r.wait(t)
	if rep.Outcome != OutcomeBudget || !strings.Contains(rep.Err, "deadline exceeded") {
		t.Fatalf("report = %+v, want budget/deadline", rep)
	}
	wait(t, s)
}

// TestPreflightSkip: a false preflight records skipped and executes nothing.
func TestPreflightSkip(t *testing.T) {
	clock := &fakeClock{now: base()}
	var runs atomic.Int32
	s, r := newScheduler(t, newStore(), clock, Task{
		Name: "gated", Interval: time.Minute,
		Preflight: func(context.Context) (bool, error) { return false, nil },
		Run: func(context.Context) (string, error) {
			runs.Add(1)
			return "", nil
		},
	})
	tick(t, s)
	if rep := r.wait(t); rep.Outcome != OutcomeSkipped {
		t.Fatalf("outcome = %s, want skipped", rep.Outcome)
	}
	wait(t, s)
	if runs.Load() != 0 {
		t.Fatalf("runs = %d, want 0", runs.Load())
	}
}

// TestPauseResume: paused time does not accumulate runs, and a pause issued
// during a run survives the run's state save.
func TestPauseResume(t *testing.T) {
	clock := &fakeClock{now: base()}
	started := make(chan struct{})
	release := make(chan struct{})
	var runs atomic.Int32
	store := newStore()
	s, r := newScheduler(t, store, clock, Task{
		Name: "pause", Interval: time.Minute,
		Run: func(context.Context) (string, error) {
			runs.Add(1)
			started <- struct{}{}
			<-release
			return "", nil
		},
	})
	if err := s.Pause(t.Context(), "pause"); err != nil {
		t.Fatal(err)
	}
	clock.set(base().Add(10 * time.Minute))
	tick(t, s)
	wait(t, s)
	if runs.Load() != 0 {
		t.Fatalf("paused runs = %d, want 0", runs.Load())
	}
	if err := s.Resume(t.Context(), "pause"); err != nil {
		t.Fatal(err)
	}
	tick(t, s)
	<-started
	if err := s.Pause(t.Context(), "pause"); err != nil {
		t.Fatal(err)
	}
	close(release)
	r.wait(t)
	wait(t, s)
	if !store.get("pause").Paused {
		t.Fatal("pause was lost when the run finished")
	}
	clock.set(clock.Now().Add(10 * time.Minute))
	tick(t, s)
	wait(t, s)
	if runs.Load() != 1 {
		t.Fatalf("runs after pause during run = %d, want 1", runs.Load())
	}
}

// TestRestartNoDuplicate: a restart keeps the consumed slot and honours the
// crash window; a clock moving backwards never re-runs a slot.
func TestRestartNoDuplicate(t *testing.T) {
	clock := &fakeClock{now: base()}
	store := newStore()
	var runs atomic.Int32
	task := Task{Name: "restart", Interval: time.Minute, Run: func(context.Context) (string, error) {
		runs.Add(1)
		return "", nil
	}}
	first, r1 := newScheduler(t, store, clock, task)
	tick(t, first)
	r1.wait(t)
	wait(t, first)

	second, _ := newScheduler(t, store, clock, task)
	clock.set(base().Add(time.Hour))
	if err := store.Save(t.Context(), "restart", &State{Next: base().Add(time.Hour), RunningUntil: base().Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	tick(t, second) // crash window from a killed run
	wait(t, second)
	if runs.Load() != 1 {
		t.Fatalf("run started inside crash window: %d", runs.Load())
	}

	clock.set(base().Add(-time.Hour)) // clock moved backwards
	tick(t, second)
	wait(t, second)
	if runs.Load() != 1 {
		t.Fatalf("backward clock re-ran a slot: %d", runs.Load())
	}
	clock.set(base().Add(3 * time.Hour))
	tick(t, second)
	wait(t, second)
	if runs.Load() != 2 {
		t.Fatalf("runs = %d, want 2 (one per due slot)", runs.Load())
	}
}

type recordingDeliverer struct {
	mu  sync.Mutex
	got []Notice
}

func (d *recordingDeliverer) Deliver(_ context.Context, n *Notice) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.got = append(d.got, *n)
	return nil
}

func (d *recordingDeliverer) notices() []Notice {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Notice(nil), d.got...)
}

// TestDeliveryOncePerSlot: a successful slot delivers once with a stable key.
func TestDeliveryOncePerSlot(t *testing.T) {
	clock := &fakeClock{now: base()}
	deliver := &recordingDeliverer{}
	s, r := newScheduler(t, newStore(), clock, Task{
		Name: "notify", Interval: time.Minute, Deliver: true,
		Run: func(context.Context) (string, error) { return "done", nil },
	})
	s.Deliver = deliver
	tick(t, s)
	r.wait(t)
	wait(t, s)
	tick(t, s)
	wait(t, s)
	got := deliver.notices()
	if len(got) != 1 {
		t.Fatalf("notices = %d, want 1", len(got))
	}
	if want := "periodic:notify:" + strconv.FormatInt(base().UnixMilli(), 10) + ":1"; got[0].IdemKey != want {
		t.Fatalf("idem key = %s, want %s", got[0].IdemKey, want)
	}
}

// TestRegisterValidation rejects tasks that would break the cadence.
func TestRegisterValidation(t *testing.T) {
	s := &Scheduler{}
	for _, task := range []Task{
		{},
		{Name: "a"},
		{Name: "a", Interval: time.Second, Jitter: time.Second, Run: func(context.Context) (string, error) { return "", nil }},
		{Name: "a", Interval: time.Second, FirstRun: -1, Run: func(context.Context) (string, error) { return "", nil }},
		{Name: "a", Interval: time.Second, Budget: -1, Run: func(context.Context) (string, error) { return "", nil }},
		{Name: "a", Interval: time.Second},
	} {
		if err := s.Register(task); err == nil {
			t.Errorf("Register(%+v) = nil, want error", task)
		}
	}
	ok := Task{Name: "a", Interval: time.Second, Run: func(context.Context) (string, error) { return "", nil }}
	if err := s.Register(ok); err != nil {
		t.Fatal(err)
	}
	if err := s.Register(ok); err == nil {
		t.Fatal("duplicate name accepted")
	}
}

// TestJitterStable: the offset depends only on the name and stays in range.
func TestJitterStable(t *testing.T) {
	j := 5 * time.Second
	seen := map[time.Duration]bool{}
	for i := range 20 {
		name := string(rune('a' + i))
		got := jitter(name, j)
		if got != jitter(name, j) || got < -j || got > j {
			t.Fatalf("jitter(%q) = %s", name, got)
		}
		seen[got] = true
	}
	if len(seen) < 2 {
		t.Fatal("jitter is not distributing names")
	}
	if jitter("a", 0) != 0 {
		t.Fatal("zero jitter must be zero")
	}
}
