package cron

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// After returns a channel that never fires: Tick tests drive time directly and
// Run is only cancelled, so no timer goroutine can leak.
func (c *fakeClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

type countedExecutor struct {
	mu    sync.Mutex
	calls int
}

func (e *countedExecutor) Run(context.Context, *Job) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	return "out", nil
}

func (e *countedExecutor) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

type recordingDeliverer struct {
	mu  sync.Mutex
	got []Delivery
}

func (d *recordingDeliverer) Deliver(_ context.Context, del *Delivery) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.got = append(d.got, *del)
	return nil
}

func (d *recordingDeliverer) deliveries() []Delivery {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Delivery(nil), d.got...)
}

type slotKey struct {
	job string
	at  int64
}

// memStore is the in-memory Store: a slot row exists at most once, exactly
// like UNIQUE(job_id, scheduled_at) in cron_runs.
type memStore struct {
	jobs  []Job
	rows  map[slotKey]*Run
	stale bool // State reports no watermark, forcing the Claim guard
}

func newMemStore(jobs ...Job) *memStore {
	return &memStore{jobs: jobs, rows: map[slotKey]*Run{}}
}

func (m *memStore) Jobs(context.Context) ([]Job, error) { return append([]Job(nil), m.jobs...), nil }

func (m *memStore) State(_ context.Context, jobID string) (State, error) {
	if m.stale {
		return State{}, nil
	}
	var st State
	for k := range m.rows {
		if k.job == jobID {
			if t := time.UnixMilli(k.at); t.After(st.Last) {
				st.Last = t
			}
		}
	}
	return st, nil
}

func (m *memStore) Claim(_ context.Context, jobID string, at time.Time, _ string) (bool, error) {
	k := slotKey{jobID, at.UnixMilli()}
	if _, ok := m.rows[k]; ok {
		return false, nil
	}
	m.rows[k] = &Run{}
	return true, nil
}

func (m *memStore) Missed(_ context.Context, jobID string, at time.Time) error {
	k := slotKey{jobID, at.UnixMilli()}
	if _, ok := m.rows[k]; ok {
		return nil
	}
	m.rows[k] = &Run{Outcome: "missed"}
	return nil
}

func (m *memStore) Finish(_ context.Context, jobID string, at time.Time, run Run) error {
	k := slotKey{jobID, at.UnixMilli()}
	r, ok := m.rows[k]
	if !ok {
		return errors.New("slot was not claimed")
	}
	*r = run
	return nil
}

func (m *memStore) outcomes(jobID, outcome string) int {
	n := 0
	for k, r := range m.rows {
		if k.job == jobID && r.Outcome == outcome {
			n++
		}
	}
	return n
}

func at(h, m int) time.Time {
	return time.Date(2026, 10, 29, h, m, 0, 0, time.UTC)
}

func testJob() Job {
	return Job{
		ID: "job-1", Name: "quarter", Schedule: "*/15 * * * *", TZ: "UTC",
		Enabled: true, CatchUp: "run_once", MaxCatchUp: 3, CreatedAt: at(10, 1),
		Script: &Script{Argv: []string{"true"}},
	}
}

// TestTickDueNotDue is the fake-clock due boundary: nothing runs one second
// before the slot and exactly one run happens at the slot.
func TestTickDueNotDue(t *testing.T) {
	store := newMemStore(testJob())
	exec := &countedExecutor{}
	clock := &fakeClock{now: at(10, 14).Add(59 * time.Second)}
	s := &Scheduler{Store: store, Executor: exec, Clock: clock}

	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := exec.count(); got != 0 {
		t.Fatalf("not-due tick ran %d jobs, want 0", got)
	}
	clock.now = at(10, 15)
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := exec.count(); got != 1 {
		t.Fatalf("due tick ran %d jobs, want 1", got)
	}
}

// TestCatchUpRestartRunsOnce: a process that was down for hours runs the
// newest slot once (default run_once) and records the rest as missed.
func TestCatchUpRestartRunsOnce(t *testing.T) {
	store := newMemStore(testJob())
	first := &Scheduler{Store: store, Executor: &countedExecutor{}, Clock: &fakeClock{now: at(10, 1)}}
	if err := first.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}

	exec := &countedExecutor{}
	deliver := &recordingDeliverer{}
	restarted := &Scheduler{Store: store, Executor: exec, Deliver: deliver, Clock: &fakeClock{now: at(12, 7)}}
	if err := restarted.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := exec.count(); got != 1 {
		t.Fatalf("catch-up ran %d slots, want 1 (run_once)", got)
	}
	if got := store.outcomes("job-1", "missed"); got != 7 {
		t.Fatalf("missed rows = %d, want 7", got)
	}
	got := deliver.deliveries()
	if len(got) != 1 || !got[0].ScheduledAt.Equal(at(12, 0)) ||
		got[0].IdemKey != "cron:job-1:"+strconv.FormatInt(at(12, 0).UnixMilli(), 10)+":1" {
		t.Fatalf("delivery = %+v, want one delivery at 12:00", got)
	}
}

func TestCatchUpPolicies(t *testing.T) {
	for _, tc := range []struct {
		policy string
		runs   int
		missed int
	}{
		{"skip", 0, 8},
		{"run_once", 1, 7},
		{"run_all", 3, 5},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			j := testJob()
			j.CatchUp = tc.policy
			store := newMemStore(j)
			exec := &countedExecutor{}
			s := &Scheduler{Store: store, Executor: exec, Clock: &fakeClock{now: at(12, 7)}}
			if err := s.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := exec.count(); got != tc.runs {
				t.Fatalf("runs = %d, want %d", got, tc.runs)
			}
			if got := store.outcomes("job-1", "missed"); got != tc.missed {
				t.Fatalf("missed = %d, want %d", got, tc.missed)
			}
		})
	}
}

// TestDeliveryIdempotent: two ticks and a restarted scheduler with a stale
// watermark still produce one execution and one delivery for the slot.
func TestDeliveryIdempotent(t *testing.T) {
	j := testJob()
	j.Schedule = "* * * * *"
	j.CreatedAt = at(10, 0)
	store := newMemStore(j)
	exec := &countedExecutor{}
	deliver := &recordingDeliverer{}
	clock := &fakeClock{now: at(10, 1).Add(30 * time.Second)}
	s := &Scheduler{Store: store, Executor: exec, Deliver: deliver, Clock: clock}
	for range 2 {
		if err := s.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	store.stale = true
	restarted := &Scheduler{Store: store, Executor: exec, Deliver: deliver, Clock: clock}
	if err := restarted.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := exec.count(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
	if got := len(deliver.deliveries()); got != 1 {
		t.Fatalf("deliveries = %d, want 1", got)
	}
}

func TestDisabledJobNotRun(t *testing.T) {
	j := testJob()
	j.Enabled = false
	store := newMemStore(j)
	exec := &countedExecutor{}
	s := &Scheduler{Store: store, Executor: exec, Clock: &fakeClock{now: at(23, 59)}}
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if exec.count() != 0 || len(store.rows) != 0 {
		t.Fatalf("disabled job ran: exec=%d rows=%d", exec.count(), len(store.rows))
	}
}
