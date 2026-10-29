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

type execFunc func(context.Context, *Job) (string, error)

func (f execFunc) Run(ctx context.Context, j *Job) (string, error) { return f(ctx, j) }

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

func (m *memStore) Active(_ context.Context, jobID string) (Active, error) {
	var a Active
	for k, r := range m.rows {
		if k.job != jobID || r.Outcome != "" {
			continue
		}
		if r.StartedAt.IsZero() {
			a.Queued++
		} else {
			a.Running++
		}
	}
	return a, nil
}

func (m *memStore) Claim(_ context.Context, jobID string, at, started time.Time, _ string) (bool, error) {
	k := slotKey{jobID, at.UnixMilli()}
	if _, ok := m.rows[k]; ok {
		return false, nil
	}
	m.rows[k] = &Run{StartedAt: started}
	return true, nil
}

func (m *memStore) TakeQueued(_ context.Context, jobID string, started time.Time) (time.Time, bool, error) {
	var (
		best  slotKey
		found bool
	)
	for k, r := range m.rows {
		if k.job != jobID || r.Outcome != "" || !r.StartedAt.IsZero() {
			continue
		}
		if !found || k.at < best.at {
			best, found = k, true
		}
	}
	if !found {
		return time.Time{}, false, nil
	}
	m.rows[best].StartedAt = started
	return time.UnixMilli(best.at), true, nil
}

func (m *memStore) Record(_ context.Context, jobID string, at time.Time, run *Run) error {
	k := slotKey{jobID, at.UnixMilli()}
	if _, ok := m.rows[k]; ok {
		return nil
	}
	m.rows[k] = run
	return nil
}

func (m *memStore) Finish(_ context.Context, jobID string, at time.Time, run *Run) error {
	k := slotKey{jobID, at.UnixMilli()}
	r, ok := m.rows[k]
	if !ok {
		return errors.New("slot was not claimed")
	}
	*r = *run
	return nil
}

func (m *memStore) outcomes(outcome string) int {
	n := 0
	for _, r := range m.rows {
		if r.Outcome == outcome {
			n++
		}
	}
	return n
}

func (m *memStore) row(jobID string, at time.Time) *Run {
	return m.rows[slotKey{jobID, at.UnixMilli()}]
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
	if got := store.outcomes("missed"); got != 7 {
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
			if got := store.outcomes("missed"); got != tc.missed {
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

// overlapJob is a one-minute job so nested ticks see the next slot while the
// current run is still open (the fake clock freezes wall time).
func overlapJob(policy string) Job {
	j := testJob()
	j.Schedule = "* * * * *"
	j.CreatedAt = at(10, 0)
	j.Overlap = policy
	return j
}

// TestOverlapSkip: the nested tick at 10:02 sees the open 10:01 run, records
// skipped_overlap and calls the executor once.
func TestOverlapSkip(t *testing.T) {
	store := newMemStore(overlapJob(OverlapSkip))
	clock := &fakeClock{now: at(10, 1)}
	var (
		s      *Scheduler
		nested error
		calls  int
	)
	exec := execFunc(func(ctx context.Context, _ *Job) (string, error) {
		calls++
		if calls == 1 {
			clock.now = at(10, 2)
			nested = s.Tick(ctx)
		}
		return "out", nil
	})
	s = &Scheduler{Store: store, Executor: exec, Clock: clock}
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if nested != nil {
		t.Fatal(nested)
	}
	if calls != 1 {
		t.Fatalf("executor calls = %d, want 1", calls)
	}
	if got := store.outcomes(OutcomeOK); got != 1 {
		t.Fatalf("ok rows = %d, want 1", got)
	}
	if got := store.outcomes(OutcomeSkippedOverlap); got != 1 {
		t.Fatalf("skipped_overlap rows = %d, want 1", got)
	}
	if len(store.rows) != 2 {
		t.Fatalf("rows = %d, want one row per slot (2)", len(store.rows))
	}
}

// TestOverlapQueueOne: 10:02 waits, 10:03 is dropped, and the queued 10:02 run
// starts after 10:01 finishes (2 runs, sequentially).
func TestOverlapQueueOne(t *testing.T) {
	store := newMemStore(overlapJob(OverlapQueueOne))
	clock := &fakeClock{now: at(10, 1)}
	var (
		s      *Scheduler
		nested error
		calls  int
	)
	exec := execFunc(func(ctx context.Context, _ *Job) (string, error) {
		calls++
		if calls > 1 {
			return "out", nil
		}
		clock.now = at(10, 2)
		nested = s.Tick(ctx)
		if nested != nil {
			return "", nested
		}
		clock.now = at(10, 3)
		nested = s.Tick(ctx)
		return "out", nil
	})
	s = &Scheduler{Store: store, Executor: exec, Clock: clock}
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if nested != nil {
		t.Fatal(nested)
	}
	if calls != 2 {
		t.Fatalf("executor calls = %d, want 2 (10:01 then queued 10:02)", calls)
	}
	if got := store.outcomes(OutcomeOK); got != 2 {
		t.Fatalf("ok rows = %d, want 2", got)
	}
	if got := store.outcomes(OutcomeSkippedOverlap); got != 1 {
		t.Fatalf("skipped_overlap rows = %d, want 1", got)
	}
	if got := len(store.rows); got != 3 {
		t.Fatalf("rows = %d, want 3 (one per slot)", got)
	}
}

// TestOverlapParallel: two runs may overlap (max 2) and the third slot is
// recorded as skipped_overlap.
func TestOverlapParallel(t *testing.T) {
	store := newMemStore(overlapJob(OverlapParallel))
	clock := &fakeClock{now: at(10, 1)}
	var (
		s      *Scheduler
		nested error
		calls  int
	)
	exec := execFunc(func(ctx context.Context, _ *Job) (string, error) {
		calls++
		switch calls {
		case 1:
			clock.now = at(10, 2)
			nested = s.Tick(ctx)
		case 2:
			clock.now = at(10, 3)
			nested = s.Tick(ctx)
		}
		return "out", nil
	})
	s = &Scheduler{Store: store, Executor: exec, Clock: clock}
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if nested != nil {
		t.Fatal(nested)
	}
	if calls != 2 {
		t.Fatalf("executor calls = %d, want 2 (parallel max)", calls)
	}
	if got := store.outcomes(OutcomeSkippedOverlap); got != 1 {
		t.Fatalf("skipped_overlap rows = %d, want 1", got)
	}
	if got := len(store.rows); got != 3 {
		t.Fatalf("rows = %d, want 3", got)
	}
}

// TestPreflightSkip: a failing preflight prevents any executor call and closes
// the slot with the reason (no half execution).
func TestPreflightSkip(t *testing.T) {
	store := newMemStore(testJob())
	exec := &countedExecutor{}
	pre := &Checks{
		DataDir:  t.TempDir(),
		Free:     func(string) (int64, int64, error) { return 1 << 40, 1 << 40, nil },
		Provider: func(context.Context) error { return errors.New("no api key") },
	}
	prompt := testJob()
	prompt.Script = nil
	prompt.CreatedAt = at(10, 1)
	store.jobs = []Job{prompt}
	s := &Scheduler{Store: store, Executor: exec, Preflight: pre, Clock: &fakeClock{now: at(10, 15)}}
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := exec.count(); got != 0 {
		t.Fatalf("executor calls = %d, want 0", got)
	}
	row := store.row("job-1", at(10, 15))
	if row == nil || row.Outcome != OutcomeGated || row.Err == "" {
		t.Fatalf("row = %+v, want gated with reason", row)
	}
}

// TestPreflightChecks covers the built-in checks: low disk and a missing data
// dir both skip, while script jobs never consult the provider.
func TestPreflightChecks(t *testing.T) {
	job := testJob()
	for _, tc := range []struct {
		name  string
		c     Checks
		job   Job
		fails bool
	}{
		{"low-water", Checks{DataDir: t.TempDir(), Free: func(string) (int64, int64, error) {
			return 100 << 20, 100 << 30, nil
		}}, job, true},
		{"enough-space", Checks{DataDir: t.TempDir(), Free: func(string) (int64, int64, error) {
			return 100 << 30, 100 << 30, nil
		}}, job, false},
		{"missing-dir", Checks{DataDir: t.TempDir() + "/absent"}, job, true},
		{
			"provider-required",
			Checks{Provider: func(context.Context) error { return errors.New("down") }},
			Job{Cwd: t.TempDir()},
			true,
		},
		{"script-skips-provider", Checks{DataDir: t.TempDir(), Provider: func(context.Context) error {
			return errors.New("down")
		}}, job, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.c.Check(t.Context(), &tc.job)
			if (err != nil) != tc.fails {
				t.Fatalf("Check() = %v, want failure=%v", err, tc.fails)
			}
		})
	}
}

// TestMonitorRecordsRun: duration, exit code and attempt land on the run row.
func TestMonitorRecordsRun(t *testing.T) {
	store := newMemStore(testJob())
	clock := &fakeClock{now: at(10, 15)}
	exec := execFunc(func(context.Context, *Job) (string, error) {
		clock.now = clock.now.Add(1500 * time.Millisecond)
		return "", &ExitError{Code: 3, Err: errors.New("boom")}
	})
	s := &Scheduler{Store: store, Executor: exec, Clock: clock}
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	row := store.row("job-1", at(10, 15))
	if row == nil {
		t.Fatal("no run row")
	}
	if row.Outcome != OutcomeFailed || row.Exit != 3 || row.Attempt != 1 {
		t.Fatalf("monitor row = %+v, want failed exit=3 attempt=1", row)
	}
	if got := row.FinishedAt.Sub(row.StartedAt); got != 1500*time.Millisecond {
		t.Fatalf("duration = %s, want 1.5s", got)
	}
}

type fakeSpawner struct {
	jobs   map[string]Spawn
	active int
	calls  int
}

func (f *fakeSpawner) Has(_ context.Context, id string) (bool, error) {
	_, ok := f.jobs[id]
	return ok, nil
}

func (f *fakeSpawner) ActiveJobs(context.Context, string) (int, error) { return f.active, nil }

func (f *fakeSpawner) Spawn(_ context.Context, spec *Spawn) (string, error) {
	f.calls++
	if _, ok := f.jobs[spec.ID]; ok {
		return spec.ID, nil
	}
	f.jobs[spec.ID] = *spec
	return spec.ID, nil
}

// TestSelfScheduleLimits: depth and quota reject a child, and the agent floor
// rejects a one-minute schedule.
func TestSelfScheduleLimits(t *testing.T) {
	spawn := &fakeSpawner{jobs: map[string]Spawn{}}
	self := &SelfSchedule{Spawner: spawn, MaxDepth: 2, MaxJobs: 1}
	parent := Parent{JobID: "job-1", At: at(10, 0), Depth: 1}
	child := Child{Name: "next", Schedule: "*/15 * * * *", Prompt: "p", Owner: "local", Cwd: "/w"}

	if _, err := self.Schedule(t.Context(), Parent{JobID: "job-1", At: at(10, 0), Depth: 2}, &child); !errors.Is(err, ErrDepth) {
		t.Fatalf("depth err = %v, want ErrDepth", err)
	}
	spawn.active = 1
	if _, err := self.Schedule(t.Context(), parent, &child); !errors.Is(err, ErrQuota) {
		t.Fatalf("quota err = %v, want ErrQuota", err)
	}
	if spawn.calls != 0 {
		t.Fatalf("spawn calls = %d, want 0", spawn.calls)
	}
}

// TestSelfScheduleIdempotent: the same (parent, scheduled_at) returns the same
// job and persists one child, even after a retry.
func TestSelfScheduleIdempotent(t *testing.T) {
	spawn := &fakeSpawner{jobs: map[string]Spawn{}}
	self := &SelfSchedule{Spawner: spawn}
	parent := Parent{JobID: "job-1", At: at(10, 0), Depth: 1}
	child := Child{Name: "next", Schedule: "0 * * * *", Prompt: "p", Owner: "local", Cwd: "/w"}

	first, err := self.Schedule(t.Context(), parent, &child)
	if err != nil {
		t.Fatal(err)
	}
	second, err := self.Schedule(t.Context(), parent, &child)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(spawn.jobs) != 1 {
		t.Fatalf("ids = %q/%q, jobs = %d; want one idempotent child", first, second, len(spawn.jobs))
	}
	other, err := self.Schedule(t.Context(), Parent{JobID: "job-1", At: at(11, 0), Depth: 1}, &child)
	if err != nil {
		t.Fatal(err)
	}
	if other == first || len(spawn.jobs) != 2 {
		t.Fatalf("next slot id = %q, jobs = %d; want a second child", other, len(spawn.jobs))
	}
}
