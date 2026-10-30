package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/scorecard"
)

// scoreWireClock is the fake clock of the wiring tests: Now is what the job
// reads; After is unused because the tests call the ticker directly.
type scoreWireClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *scoreWireClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *scoreWireClock) set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

func (c *scoreWireClock) After(time.Duration) <-chan time.Time { return nil }

type scoreWireRunner struct {
	mu    sync.Mutex
	calls int
	fail  bool
}

func (r *scoreWireRunner) Run(context.Context, scorecard.Request) (scorecard.Report, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.fail {
		return scorecard.Report{}, errors.New("harness down")
	}
	return scorecard.Report{
		Score:      0.9,
		Dimensions: []scorecard.Dimension{{Name: "e2e", Score: 0.9}},
	}, nil
}

func (r *scoreWireRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func openScoreDB(t *testing.T) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close db: %v", cerr)
		}
	})
	return db
}

func scoreWireCron(t *testing.T, db *ctxdb.Store, clock *scoreWireClock) *serveCron {
	t.Helper()
	cr := newServeCron(t.Context(), db, t.TempDir(), "", true, nil, io.Discard)
	cr.clock = clock
	cr.sched.Clock = clock
	cr.score.Clock = clock
	return cr
}

// TestServeScorecardOncePerISOWeek: two ticks in the same week run the
// harness once; the next Monday runs it again.
func TestServeScorecardOncePerISOWeek(t *testing.T) {
	t.Parallel()
	db := openScoreDB(t)
	monday := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	clock := &scoreWireClock{now: monday}
	cr := scoreWireCron(t, db, clock)
	runner := &scoreWireRunner{}
	cr.score.Runner = runner
	ctx := t.Context()

	for range 2 {
		if err := cr.tick(ctx); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}
	if got := runner.count(); got != 1 {
		t.Fatalf("runner calls in one week = %d, want 1", got)
	}
	last, err := (&scorecardStore{db: db}).Last(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if last.Week != "2026-W44" || last.Status != scorecard.StatusOK {
		t.Fatalf("first result = %s/%s, want 2026-W44/ok", last.Week, last.Status)
	}

	clock.set(monday.AddDate(0, 0, 7))
	for range 2 {
		if terr := cr.tick(ctx); terr != nil {
			t.Fatalf("next week tick: %v", terr)
		}
	}
	if got := runner.count(); got != 2 {
		t.Fatalf("runner calls after next Monday = %d, want 2", got)
	}
	last, err = (&scorecardStore{db: db}).Last(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if last.Week != "2026-W45" {
		t.Fatalf("next week result = %s, want 2026-W45", last.Week)
	}
}

// TestServeScorecardWithoutRunnerIsSkipped: no harness means skipped with no
// invented score, and no alarm.
func TestServeScorecardWithoutRunnerIsSkipped(t *testing.T) {
	t.Parallel()
	db := openScoreDB(t)
	clock := &scoreWireClock{now: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)}
	cr := scoreWireCron(t, db, clock)
	cr.score.Runner = nil
	ctx := t.Context()
	if err := cr.tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	res, err := (&scorecardStore{db: db}).Last(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != scorecard.StatusSkipped || res.Score != 0 {
		t.Fatalf("result = %s score=%v, want skipped/0", res.Status, res.Score)
	}
	if res.Error != scorecard.ErrRunnerUnavailable.Error() {
		t.Fatalf("error = %q, want %q", res.Error, scorecard.ErrRunnerUnavailable.Error())
	}
	if n := countRows(t, db, `SELECT count(*) FROM gateway_deliveries`); n != 0 {
		t.Fatalf("outbox rows = %d, want 0 for a skipped run", n)
	}
}

// TestServeScorecardFailureAlarmsOnce: a failing run enqueues exactly one
// owner alarm, and repeating the same week adds none.
func TestServeScorecardFailureAlarmsOnce(t *testing.T) {
	t.Parallel()
	db := openScoreDB(t)
	clock := &scoreWireClock{now: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)}
	cr := scoreWireCron(t, db, clock)
	cr.score.Runner = &scoreWireRunner{fail: true}
	cr.score.Owner = scorecard.Owner{Platform: "telegram", ChatID: "42"}
	ctx := t.Context()
	for range 3 {
		if err := cr.tick(ctx); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}
	if n := countRows(t, db, `SELECT count(*) FROM gateway_deliveries`); n != 1 {
		t.Fatalf("outbox rows = %d, want 1 alarm", n)
	}
	res, err := (&scorecardStore{db: db}).Last(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != scorecard.StatusFailed {
		t.Fatalf("result status = %s, want failed", res.Status)
	}
	if res.Alarm.Status != scorecard.StatusOK {
		t.Fatalf("alarm status = %s, want ok", res.Alarm.Status)
	}
	var body string
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT body FROM gateway_deliveries LIMIT 1`, nil, &body)
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "failed") || !strings.Contains(body, "harness down") {
		t.Fatalf("alarm body = %q, want failed + harness down", body)
	}
}

// TestServeScorecardAlarmFallsBackToHomeChannel: with no owner env the alarm
// goes to the GW-005 home channel.
func TestServeScorecardAlarmFallsBackToHomeChannel(t *testing.T) {
	t.Parallel()
	db := openScoreDB(t)
	clock := &scoreWireClock{now: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)}
	cr := scoreWireCron(t, db, clock)
	cr.score.Runner = &scoreWireRunner{fail: true}
	cr.score.Owner = scorecard.Owner{}
	if err := (gwstore.Store{DB: db}).SetHomeChannel(t.Context(), "telegram", "99", "", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := cr.tick(t.Context()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	var chat string
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT chat_id FROM gateway_deliveries LIMIT 1`, nil, &chat)
	}); err != nil {
		t.Fatal(err)
	}
	if chat != "99" {
		t.Fatalf("alarm chat = %q, want the home channel 99", chat)
	}
}

// TestScorecardCLIRunsOnceAndShowsStatus: the manual command runs one week
// once and both run/status render the stored result.
func TestScorecardCLIRunsOnceAndShowsStatus(t *testing.T) {
	dir := t.TempDir()
	first := scorecardCLIRun(t, dir)
	if first.Status != scorecard.StatusSkipped || first.Week == "" {
		t.Fatalf("first run = %s/%s, want a week with skipped", first.Week, first.Status)
	}
	second := scorecardCLIRunAgain(t, dir)
	if second.Week != first.Week || second.Status != scorecard.StatusSkipped {
		t.Fatalf("second run = %s/%s, want %s/skipped", second.Week, second.Status, first.Week)
	}
	if out := scorecardCLIStatus(t, dir); !strings.Contains(out, first.Week) || !strings.Contains(out, "skipped") {
		t.Fatalf("status output = %q, want week + skipped", out)
	}
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close db: %v", cerr)
		}
	}()
	runs := countRows(t, db, `SELECT count(*) FROM module_state WHERE module_id = 'scorecard' AND key LIKE 'run:%'`)
	claims := countRows(t, db, `SELECT count(*) FROM module_state WHERE module_id = 'scorecard' AND key LIKE 'claim:%'`)
	if runs != 1 || claims != 1 {
		t.Fatalf("stored runs/claims = %d/%d, want 1/1", runs, claims)
	}
}

func scorecardCLIRun(t *testing.T, dir string) scorecard.Result {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := runScorecard([]string{"run", "--data-dir", dir, "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("first run = %d (%s)", code, errOut.String())
	}
	var res scorecard.Result
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("first run output is not JSON: %v (%s)", err, out.String())
	}
	return res
}

func scorecardCLIRunAgain(t *testing.T, dir string) scorecard.Result {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := runScorecard([]string{"run", "--data-dir", dir, "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("second run = %d (%s)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "already ran") {
		t.Fatalf("second run stderr = %q, want already ran", errOut.String())
	}
	var res scorecard.Result
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("second run output is not JSON: %v", err)
	}
	return res
}

func scorecardCLIStatus(t *testing.T, dir string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := runScorecard([]string{"status", "--data-dir", dir}, &out, &errOut); code != 0 {
		t.Fatalf("status = %d (%s)", code, errOut.String())
	}
	return out.String()
}

func countRows(t *testing.T, db *ctxdb.Store, query string) int {
	t.Helper()
	var n int
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(query, nil, &n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}
