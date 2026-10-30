package scorecard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func monday() time.Time { return time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC) }

func nextWeek() time.Time { return monday().AddDate(0, 0, 7) }

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

type finished struct {
	key string
	res Result
}

type fakeStore struct {
	claimed  map[string]bool
	claims   int
	finished []finished
	last     Result
	hasLast  bool
	claimErr error
	lastErr  error
	finErr   error
}

func newFakeStore() *fakeStore { return &fakeStore{claimed: map[string]bool{}} }

func (s *fakeStore) Claim(_ context.Context, key string) (bool, error) {
	if s.claimErr != nil {
		return false, s.claimErr
	}
	if s.claimed[key] {
		return false, nil
	}
	s.claimed[key], s.claims = true, s.claims+1
	return true, nil
}

func (s *fakeStore) Finish(_ context.Context, key string, r *Result) error {
	if s.finErr != nil {
		return s.finErr
	}
	s.finished = append(s.finished, finished{key: key, res: *r})
	if r.Status == StatusOK {
		s.last, s.hasLast = *r, true
	}
	return nil
}

func (s *fakeStore) LastOK(context.Context) (Result, error) {
	if s.lastErr != nil {
		return Result{}, s.lastErr
	}
	if !s.hasLast {
		return Result{}, ErrNoRun
	}
	return s.last, nil
}

type fakeRunner struct {
	calls int
	reqs  []Request
	fn    func(Request) (Report, error)
}

func (r *fakeRunner) Run(_ context.Context, req Request) (Report, error) {
	r.calls++
	r.reqs = append(r.reqs, req)
	if r.fn != nil {
		return r.fn(req)
	}
	return Report{Score: 1, Dimensions: []Dimension{{Name: "e2e", Score: 1}}}, nil
}

type fakeOutbox struct {
	msgs []Message
	err  error
}

func (o *fakeOutbox) Enqueue(_ context.Context, m Message) error {
	if o.err != nil {
		return o.err
	}
	o.msgs = append(o.msgs, m)
	return nil
}

func newJob(clock *fakeClock, store *fakeStore, runner Runner, out *fakeOutbox) *Job {
	return &Job{
		Clock:  clock,
		Store:  store,
		Runner: runner,
		Outbox: out,
		Owner:  Owner{Platform: "telegram", ChatID: "42"},
	}
}

func TestWeekKey(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		at    time.Time
		week  string
		start time.Time
	}{
		{monday(), "2026-W44", monday()},
		{monday().Add(23 * time.Hour), "2026-W44", monday()},
		{monday().AddDate(0, 0, 6), "2026-W44", monday()},
		{nextWeek(), "2026-W45", nextWeek()},
		// 2026-01-01 is Thursday: ISO week 1 starts Monday 2025-12-29.
		{time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), "2026-W01", time.Date(2025, 12, 29, 0, 0, 0, 0, time.UTC)},
	} {
		if got := WeekKey(tc.at); got != tc.week {
			t.Errorf("WeekKey(%s) = %s, want %s", tc.at, got, tc.week)
		}
		if got := WeekStart(tc.at); !got.Equal(tc.start) {
			t.Errorf("WeekStart(%s) = %s, want %s", tc.at, got, tc.start)
		}
	}
	if got, want := IdemKey("2026-W44"), "scorecard-weekly:2026-W44"; got != want {
		t.Errorf("IdemKey = %q, want %q", got, want)
	}
	if got, want := AlarmKey("2026-W44"), "scorecard-weekly:2026-W44:alarm"; got != want {
		t.Errorf("AlarmKey = %q, want %q", got, want)
	}
}

func tickOnce(t *testing.T, job *Job) Result {
	t.Helper()
	res, ran, err := job.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: err=%v", err)
	}
	if !ran {
		t.Fatal("Tick did not run")
	}
	return res
}

func tickNoop(t *testing.T, job *Job) {
	t.Helper()
	_, ran, err := job.Tick(context.Background())
	if err != nil {
		t.Fatalf("replay Tick: err=%v", err)
	}
	if ran {
		t.Fatal("replay Tick ran again")
	}
}

func TestTickOncePerWeek(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{t: monday()}
	store, runner, out := newFakeStore(), &fakeRunner{}, &fakeOutbox{}
	job := newJob(clock, store, runner, out)

	res := tickOnce(t, job)
	if res.Week != "2026-W44" {
		t.Errorf("week = %q", res.Week)
	}
	if res.Status != StatusOK {
		t.Errorf("status = %q", res.Status)
	}
	if res.Alarm.Status != StatusSkipped {
		t.Errorf("alarm = %+v", res.Alarm)
	}
	if runner.calls != 1 {
		t.Errorf("calls = %d", runner.calls)
	}
	if len(store.finished) != 1 {
		t.Fatalf("finished = %d", len(store.finished))
	}
	req := runner.reqs[0]
	if req.Seed != DefaultSeed || req.MaxCostUSD != DefaultMaxCostUSD {
		t.Errorf("request = %+v, want fixed seed and US$5 cap", req)
	}
	tickNoop(t, job)
	if runner.calls != 1 {
		t.Errorf("replay ran the runner: calls=%d", runner.calls)
	}
	if len(out.msgs) != 0 {
		t.Errorf("replay queued messages: %d", len(out.msgs))
	}
}

func TestTickNextWeekRunsAgain(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{t: monday()}
	store, runner, out := newFakeStore(), &fakeRunner{}, &fakeOutbox{}
	job := newJob(clock, store, runner, out)
	tickOnce(t, job)

	clock.t = nextWeek()
	res := tickOnce(t, job)
	if res.Week != "2026-W45" {
		t.Errorf("week = %q", res.Week)
	}
	if runner.calls != 2 || len(store.finished) != 2 {
		t.Fatalf("calls=%d finished=%d, want 2/2", runner.calls, len(store.finished))
	}
	if got := store.finished[1].key; got != IdemKey("2026-W45") {
		t.Errorf("finish key = %q", got)
	}
	if len(out.msgs) != 0 {
		t.Errorf("unexpected messages: %d", len(out.msgs))
	}
}

func TestTickRunnerErrorAlarmsOnce(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{t: monday()}
	store, out := newFakeStore(), &fakeOutbox{}
	runner := &fakeRunner{fn: func(Request) (Report, error) { return Report{}, errors.New("harness boom") }}
	job := newJob(clock, store, runner, out)

	res, _, err := job.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.Status != StatusFailed {
		t.Errorf("status = %q", res.Status)
	}
	if res.Error != "harness boom" {
		t.Errorf("error = %q", res.Error)
	}
	if res.Alarm.Status != StatusOK {
		t.Errorf("alarm = %+v", res.Alarm)
	}
	if len(out.msgs) != 1 {
		t.Fatalf("messages = %d", len(out.msgs))
	}
	m := out.msgs[0]
	if m.Key != AlarmKey("2026-W44") || m.Platform != "telegram" || m.ChatID != "42" {
		t.Errorf("message target/key = %+v", m)
	}
	if !strings.Contains(m.Body, "failed") || !strings.Contains(m.Body, "harness boom") {
		t.Errorf("body = %q", m.Body)
	}
	tickNoop(t, job)
	if len(out.msgs) != 1 {
		t.Errorf("alarm replayed: %d", len(out.msgs))
	}
	if runner.calls != 1 {
		t.Errorf("runner replayed: %d", runner.calls)
	}
}

func TestTickWithoutRunnerSkips(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{t: monday()}
	store, out := newFakeStore(), &fakeOutbox{}
	job := newJob(clock, store, nil, out)

	res, ran, err := job.Tick(context.Background())
	if err != nil || !ran {
		t.Fatalf("Tick: ran=%v err=%v", ran, err)
	}
	if res.Status != StatusSkipped || !strings.Contains(res.Error, "runner tidak tersedia") {
		t.Fatalf("result = %+v, want skipped runner-unavailable", res)
	}
	if res.Score != 0 || len(res.Dimensions) != 0 {
		t.Errorf("skipped run invented a score: %+v", res)
	}
	if res.Alarm.Status != StatusSkipped || len(out.msgs) != 0 {
		t.Errorf("skipped run alarmed: %+v, messages=%d", res.Alarm, len(out.msgs))
	}
	if len(store.finished) != 1 || store.finished[0].res.Status != StatusSkipped {
		t.Errorf("skipped result not stored: %+v", store.finished)
	}
}

func TestTickNoOwnerChatSkipsAlarm(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{t: monday()}
	store, out := newFakeStore(), &fakeOutbox{}
	runner := &fakeRunner{fn: func(Request) (Report, error) { return Report{}, errors.New("boom") }}
	job := newJob(clock, store, runner, out)
	job.Owner = Owner{}

	res, _, err := job.Tick(context.Background())
	if err != nil || res.Status != StatusFailed {
		t.Fatalf("Tick: res=%+v err=%v", res, err)
	}
	if res.Alarm.Status != StatusSkipped || res.Alarm.Reason != "no owner chat" || len(out.msgs) != 0 {
		t.Fatalf("alarm = %+v, messages=%d", res.Alarm, len(out.msgs))
	}
}

func TestTickDimensionRegressionAlarms(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{t: monday()}
	store, out := newFakeStore(), &fakeOutbox{}
	runner := &fakeRunner{}
	job := newJob(clock, store, runner, out)
	if _, _, err := job.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}

	clock.t = nextWeek()
	runner.fn = func(Request) (Report, error) {
		return Report{Score: 1, Dimensions: []Dimension{{Name: "e2e", Score: 0.9}, {Name: "speed", Score: 1}}}, nil
	}
	res, _, err := job.Tick(context.Background())
	if err != nil || res.Status != StatusOK {
		t.Fatalf("Tick: res=%+v err=%v", res, err)
	}
	if len(res.Regressions) != 1 || !strings.Contains(res.Regressions[0], "dimension e2e") {
		t.Fatalf("regressions = %v", res.Regressions)
	}
	if res.Alarm.Status != StatusOK || len(out.msgs) != 1 || !strings.Contains(out.msgs[0].Body, "regressions=") {
		t.Fatalf("alarm = %+v messages=%d", res.Alarm, len(out.msgs))
	}
	if res.Dimensions[0].Name != "e2e" || res.Dimensions[1].Name != "speed" {
		t.Errorf("dimensions not sorted: %+v", res.Dimensions)
	}
}

func TestTickSmallRegressionDoesNotAlarm(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{t: monday()}
	store, out := newFakeStore(), &fakeOutbox{}
	runner := &fakeRunner{}
	job := newJob(clock, store, runner, out)
	if _, _, err := job.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}

	clock.t = nextWeek()
	runner.fn = func(Request) (Report, error) {
		return Report{Score: 0.96, Dimensions: []Dimension{{Name: "e2e", Score: 0.96}}}, nil
	}
	res, _, err := job.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Regressions) != 0 || res.Alarm.Status != StatusSkipped || len(out.msgs) != 0 {
		t.Fatalf("4%% drop alarmed: regressions=%v alarm=%+v messages=%d", res.Regressions, res.Alarm, len(out.msgs))
	}
}

func TestTickInfraErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := &fakeClock{t: monday()}
	if _, _, err := (&Job{Clock: clock}).Tick(ctx); !errors.Is(err, ErrNoStore) {
		t.Errorf("nil store err = %v, want ErrNoStore", err)
	}
	lastErr := errors.New("last")
	store := newFakeStore()
	store.lastErr = lastErr
	if _, ran, err := newJob(clock, store, &fakeRunner{}, &fakeOutbox{}).Tick(ctx); !errors.Is(err, lastErr) || ran {
		t.Errorf("LastOK error: ran=%v err=%v", ran, err)
	}
	if store.claims != 0 {
		t.Errorf("claimed after LastOK error: %d", store.claims)
	}
	claimErr := errors.New("claim")
	store = newFakeStore()
	store.claimErr = claimErr
	if _, _, err := newJob(clock, store, &fakeRunner{}, &fakeOutbox{}).Tick(ctx); !errors.Is(err, claimErr) {
		t.Errorf("claim error = %v", err)
	}
	finErr := errors.New("finish")
	store = newFakeStore()
	store.finErr = finErr
	if _, ran, err := newJob(clock, store, &fakeRunner{}, &fakeOutbox{}).Tick(ctx); !ran || !errors.Is(err, finErr) {
		t.Errorf("finish error: ran=%v err=%v", ran, err)
	}
	store = newFakeStore()
	runner := &fakeRunner{fn: func(Request) (Report, error) { return Report{}, errors.New("boom") }}
	if _, _, err := newJob(clock, store, runner, &fakeOutbox{err: errors.New("outbox")}).Tick(ctx); err == nil {
		t.Error("outbox error not returned")
	}
}

func TestResultFormats(t *testing.T) {
	t.Parallel()
	res := Result{
		Week: "2026-W44", WeekStart: monday(), Status: StatusOK, Seed: 7,
		Score: 0.75, Dimensions: []Dimension{{Name: "speed", Score: 0.8}, {Name: "e2e", Score: 0.7}},
		Alarm: Alarm{Status: StatusSkipped, Reason: "no alarm condition"},
	}
	var jsonBuf bytes.Buffer
	if err := res.WriteJSON(&jsonBuf); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(jsonBuf.Bytes(), &got); err != nil {
		t.Fatalf("json: %v: %s", err, jsonBuf.String())
	}
	if got["week"] != "2026-W44" || got["status"] != "ok" {
		t.Errorf("json = %s", jsonBuf.String())
	}
	var csvBuf bytes.Buffer
	if err := res.WriteCSV(&csvBuf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(csvBuf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("csv lines = %d: %q", len(lines), csvBuf.String())
	}
	if lines[0] != "week,status,seed,score,dimension,value" || !strings.HasPrefix(lines[1], "2026-W44,ok,7,0.75,") {
		t.Errorf("csv header/overall = %q", lines[:2])
	}
	if !strings.HasSuffix(lines[2], ",speed,0.8") || !strings.HasSuffix(lines[3], ",e2e,0.7") {
		t.Errorf("csv dimensions = %q", lines[2:])
	}
	if body := Summary(&res); !strings.Contains(body, "scorecard-weekly 2026-W44: ok") {
		t.Errorf("Summary = %q", body)
	}
}

func TestOwnerFromEnv(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want Owner
		ok   bool
	}{
		{"telegram:42", Owner{"telegram", "42"}, true},
		{" telegram:42 , telegram:43 ", Owner{"telegram", "42"}, true},
		{"", Owner{}, false},
		{"telegram", Owner{}, false},
		{":42", Owner{}, false},
		{"telegram:", Owner{}, false},
		{"telegram:4\n2", Owner{}, false},
	} {
		got, ok := OwnerFromEnv(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("OwnerFromEnv(%q) = %+v,%v want %+v,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

type fakeHome struct {
	owner Owner
	ok    bool
	err   error
}

func (h fakeHome) HomeChannel(context.Context) (Owner, bool, error) { return h.owner, h.ok, h.err }

// TestTickNoOwnerUsesHomeChannel: an unset Owner falls back to the gateway
// /sethome channel (GW-005), so a cron alarm still reaches the owner.
func TestTickNoOwnerUsesHomeChannel(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{t: monday()}
	store, out := newFakeStore(), &fakeOutbox{}
	runner := &fakeRunner{fn: func(Request) (Report, error) { return Report{}, errors.New("boom") }}
	job := newJob(clock, store, runner, out)
	job.Owner = Owner{}
	job.Home = fakeHome{owner: Owner{Platform: "telegram", ChatID: "99"}, ok: true}

	res, _, err := job.Tick(context.Background())
	if err != nil || res.Alarm.Status != StatusOK || len(out.msgs) != 1 {
		t.Fatalf("alarm=%+v msgs=%d err=%v", res.Alarm, len(out.msgs), err)
	}
	if out.msgs[0].Platform != "telegram" || out.msgs[0].ChatID != "99" {
		t.Fatalf("alarm target = %+v, want home telegram:99", out.msgs[0])
	}
}
