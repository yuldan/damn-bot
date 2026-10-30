package cron

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type memIncidents struct {
	m map[string]Incident
}

func newMemIncidents() *memIncidents { return &memIncidents{m: map[string]Incident{}} }

func (s *memIncidents) Incident(_ context.Context, jobID string) (Incident, error) {
	return s.m[jobID], nil
}

func (s *memIncidents) SaveIncident(_ context.Context, jobID string, inc Incident) error {
	s.m[jobID] = inc
	return nil
}

type recordingAlerter struct {
	mu   sync.Mutex
	got  []Alarm
	fail error
}

func (a *recordingAlerter) Alert(_ context.Context, al *Alarm) error {
	if a.fail != nil {
		return a.fail
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, *al)
	return nil
}

func (a *recordingAlerter) alarms() []Alarm {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Alarm(nil), a.got...)
}

// TestIncidentAlarmOncePerHour is the AUTO-008 rate table: 3 consecutive
// failures raise one alarm, a fourth in the same hour raises none, and the
// next hour may alarm again.
func TestIncidentAlarmOncePerHour(t *testing.T) {
	clock := &fakeClock{now: at(10, 0)}
	st, al := newMemIncidents(), &recordingAlerter{}
	r := &Recorder{Store: st, Alarm: al, Clock: clock}
	for i := 1; i <= 4; i++ {
		if err := r.Failure(t.Context(), "job-1", "quarter", IncidentFailed, "boom"); err != nil {
			t.Fatal(err)
		}
		if got := len(al.alarms()); got != min(i/3, 1) {
			t.Fatalf("after failure %d: alarms = %d, want %d", i, got, min(i/3, 1))
		}
		clock.now = clock.now.Add(10 * time.Minute)
	}
	if inc := st.m["job-1"]; inc.Failures != 4 || inc.Kind != IncidentFailed {
		t.Fatalf("incident = %+v, want 4 failed", inc)
	}
	clock.now = at(11, 20)
	if err := r.Failure(t.Context(), "job-1", "quarter", IncidentFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	if got := len(al.alarms()); got != 2 {
		t.Fatalf("alarms after the hour = %d, want 2", got)
	}
}

// TestIncidentSuccessResetsStreakButKeepsHour: a success clears the consecutive
// count; the hourly cap still belongs to the job, so a new 3-streak inside the
// same hour stays silent.
func TestIncidentSuccessResetsStreak(t *testing.T) {
	clock := &fakeClock{now: at(10, 0)}
	st, al := newMemIncidents(), &recordingAlerter{}
	r := &Recorder{Store: st, Alarm: al, Clock: clock}
	for range 3 {
		if err := r.Failure(t.Context(), "job-1", "quarter", IncidentFailed, "boom"); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(al.alarms()); got != 1 {
		t.Fatalf("alarms = %d, want 1", got)
	}
	if err := r.Success(t.Context(), "job-1"); err != nil {
		t.Fatal(err)
	}
	inc := st.m["job-1"]
	if inc.Failures != 0 || inc.Kind != "" || inc.Err != "" {
		t.Fatalf("after success incident = %+v, want cleared streak", inc)
	}
	if inc.LastAlarm.IsZero() {
		t.Fatal("success dropped the hourly alarm clock")
	}
	clock.now = clock.now.Add(time.Minute)
	for range 3 {
		if err := r.Failure(t.Context(), "job-1", "quarter", IncidentFailed, "boom"); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(al.alarms()); got != 1 {
		t.Fatalf("alarms after reset inside the hour = %d, want 1", got)
	}
}

// TestIncidentAlarmRetriesWhenRejected: a failed alarm does not consume the
// hourly budget, so the next failure tries again.
func TestIncidentAlarmRetriesWhenRejected(t *testing.T) {
	clock := &fakeClock{now: at(10, 0)}
	st, al := newMemIncidents(), &recordingAlerter{fail: errors.New("outbox down")}
	r := &Recorder{Store: st, Alarm: al, Clock: clock}
	for i := 1; i <= 3; i++ {
		err := r.Failure(t.Context(), "job-1", "quarter", IncidentFailed, "boom")
		if (i >= 3) != (err != nil) {
			t.Fatalf("failure %d error = %v, want error only when the alarm fires", i, err)
		}
	}
	if !st.m["job-1"].LastAlarm.IsZero() {
		t.Fatal("rejected alarm consumed the hourly window")
	}
	al.fail = nil
	if err := r.Failure(t.Context(), "job-1", "quarter", IncidentFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	if got := len(al.alarms()); got != 1 {
		t.Fatalf("alarms after recovery = %d, want 1", got)
	}
}

// failingExec fails the first n calls, then succeeds.
type failingExec struct {
	n int
}

func (e *failingExec) Run(context.Context, *Job) (string, error) {
	if e.n > 0 {
		e.n--
		return "partial", fmt.Errorf("executor: boom %d", e.n)
	}
	return "ok output", nil
}

// dedupeDeliverer keeps one delivery per idempotency key, like the outbox.
type dedupeDeliverer struct {
	mu   sync.Mutex
	keys map[string]int
	off  bool
}

func (d *dedupeDeliverer) Deliver(_ context.Context, m *Delivery) error {
	if d.off {
		return fmt.Errorf("%w: network.offline", ErrOffline)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.keys == nil {
		d.keys = map[string]int{}
	}
	d.keys[m.IdemKey]++
	return nil
}

func (d *dedupeDeliverer) count(key string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.keys[key]
}

// TestTickDeliveryOnceAcrossRestart proves the slot key is the delivery
// boundary: a second Tick and a restarted Scheduler never enqueue it again.
func TestTickDeliveryOnceAcrossRestart(t *testing.T) {
	job := testJob()
	store := newMemStore(job)
	clock := &fakeClock{now: at(10, 15)}
	deliver := &dedupeDeliverer{}
	s := &Scheduler{Store: store, Executor: execFunc(func(context.Context, *Job) (string, error) {
		return "report", nil
	}), Deliver: deliver, Clock: clock}
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("cron:%s:%d:1", job.ID, at(10, 15).UnixMilli())
	if got := deliver.count(key); got != 1 {
		t.Fatalf("deliveries = %d, want 1", got)
	}
	restarted := &Scheduler{Store: store, Executor: execFunc(func(context.Context, *Job) (string, error) {
		return "report", nil
	}), Deliver: deliver, Clock: clock}
	for range 2 {
		if err := restarted.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := deliver.count(key); got != 1 {
		t.Fatalf("deliveries after restart = %d, want 1", got)
	}
	if row := store.row(job.ID, at(10, 15)); row == nil || row.Delivery != "queued" || row.Incident != "" {
		t.Fatalf("slot row = %+v, want delivery queued and no incident", row)
	}
}

// TestTickOfflineMarksSkipped: an offline gateway queues nothing, records the
// reason on the slot and does not start an incident streak.
func TestTickOfflineMarksSkipped(t *testing.T) {
	job := testJob()
	store := newMemStore(job)
	al := &recordingAlerter{}
	s := &Scheduler{
		Store: store, Executor: execFunc(func(context.Context, *Job) (string, error) { return "x", nil }),
		Deliver: &dedupeDeliverer{off: true}, Incidents: newMemIncidents(), Alert: al,
		Clock: &fakeClock{now: at(10, 15)},
	}
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	row := store.row(job.ID, at(10, 15))
	if row == nil || row.Delivery == "" || !strings.HasPrefix(row.Delivery, "skipped:") || row.Incident != "" {
		t.Fatalf("slot row = %+v, want skipped delivery and no incident", row)
	}
	if got := len(al.alarms()); got != 0 {
		t.Fatalf("offline raised %d alarms, want 0", got)
	}
}

type offlineAlerter struct{}

func (offlineAlerter) Alert(context.Context, *Alarm) error {
	return fmt.Errorf("%w: gateway down", ErrOffline)
}

// TestIncidentOfflineAlarmIsRetried: an alarm that cannot be queued because the
// gateway is offline neither fails the tick nor consumes the hourly window.
func TestIncidentOfflineAlarmIsRetried(t *testing.T) {
	clock := &fakeClock{now: at(10, 0)}
	st := newMemIncidents()
	r := &Recorder{Store: st, Alarm: offlineAlerter{}, Clock: clock}
	for range 3 {
		if err := r.Failure(t.Context(), "job-1", "quarter", IncidentFailed, "boom"); err != nil {
			t.Fatal(err)
		}
	}
	if !st.m["job-1"].LastAlarm.IsZero() {
		t.Fatal("offline alarm consumed the hourly window")
	}
	al := &recordingAlerter{}
	r.Alarm = al
	if err := r.Failure(t.Context(), "job-1", "quarter", IncidentFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	if got := len(al.alarms()); got != 1 {
		t.Fatalf("alarms after reconnect = %d, want 1", got)
	}
}

// TestTickFailuresAlarmAndReset is the scheduler-level incident flow: three
// consecutive failed slots alarm once, a fourth stays silent, and success
// clears the streak.
func TestTickFailuresAlarmAndReset(t *testing.T) {
	job := testJob()
	store := newMemStore(job)
	st, al := newMemIncidents(), &recordingAlerter{}
	clock := &fakeClock{now: at(10, 15)}
	exec := &failingExec{n: 4}
	s := &Scheduler{
		Store: store, Executor: exec, Incidents: st, Alert: al, Clock: clock,
	}
	for i := 1; i <= 4; i++ {
		if err := s.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		want := min(i/3, 1)
		if got := len(al.alarms()); got != want {
			t.Fatalf("after slot %d: alarms = %d, want %d", i, got, want)
		}
		clock.now = clock.now.Add(15 * time.Minute)
	}
	row := store.row(job.ID, at(10, 15))
	if row == nil || row.Outcome != OutcomeFailed || row.Incident != IncidentFailed {
		t.Fatalf("slot row = %+v, want failed incident", row)
	}
	if inc := st.m[job.ID]; inc.Failures != 4 {
		t.Fatalf("streak = %d, want 4", inc.Failures)
	}
	if err := s.Tick(t.Context()); err != nil { // success resets
		t.Fatal(err)
	}
	if inc := st.m[job.ID]; inc.Failures != 0 || inc.Kind != "" {
		t.Fatalf("after success incident = %+v, want reset", inc)
	}
}

// TestTickPreflightIncident: a gated slot is recorded as preflight, not failed.
func TestTickPreflightIncident(t *testing.T) {
	job := testJob()
	store := newMemStore(job)
	st := newMemIncidents()
	s := &Scheduler{
		Store: store, Executor: &countedExecutor{}, Preflight: failCheck{},
		Incidents: st, Clock: &fakeClock{now: at(10, 15)},
	}
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if row := store.row(job.ID, at(10, 15)); row == nil || row.Outcome != OutcomeGated || row.Incident != IncidentPreflight {
		t.Fatalf("slot row = %+v, want gated preflight", row)
	}
	if inc := st.m[job.ID]; inc.Kind != IncidentPreflight || inc.Failures != 1 {
		t.Fatalf("incident = %+v, want preflight streak 1", inc)
	}
}

type failCheck struct{}

func (failCheck) Check(context.Context, *Job) error { return errors.New("disk low-water") }
