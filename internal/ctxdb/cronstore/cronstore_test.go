package cronstore

import (
	"fmt"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func open(t *testing.T) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	})
	return db
}

func addJob(t *testing.T, db *ctxdb.Store, id string, enabled bool) {
	t.Helper()
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO cron_jobs(id, name, schedule, prompt, prompt_sha, cwd, enabled, created_at, updated_at)
			VALUES (?1, ?1, '*/15 * * * *', 'p', 'sha-' || ?1, '/w', ?2, 1000, 1000)`, id, enabled)
		if xerr != nil {
			return fmt.Errorf("insert job: %w", xerr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func slot(m int) time.Time {
	return time.Date(2026, 10, 29, 10, m, 0, 0, time.UTC)
}

// TestClaimOncePerSlot is the store-side idempotency: a second claim for the
// same (job, scheduled_at) loses, including across a new Store value.
func TestClaimOncePerSlot(t *testing.T) {
	db := open(t)
	addJob(t, db, "job-1", true)
	s := Store{DB: db}
	for i, want := range []bool{true, false} {
		got, err := s.Claim(t.Context(), "job-1", slot(15), slot(15), "pid@host")
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("claim #%d = %v, want %v", i+1, got, want)
		}
	}
	again := Store{DB: db} // restart: the row is still the owner
	if got, err := again.Claim(t.Context(), "job-1", slot(15), slot(15), "other@host"); err != nil || got {
		t.Fatalf("restart claim = %v, %v; want false, nil", got, err)
	}
	if ok, err := s.Claim(t.Context(), "job-1", slot(20), slot(20), "pid@host"); err != nil || !ok {
		t.Fatalf("claim of another slot = %v, %v; want true, nil", ok, err)
	}
	if n := runCount(t, db); n != 2 {
		t.Fatalf("cron_runs rows = %d, want 2", n)
	}
}

func runCount(t *testing.T, db *ctxdb.Store) int {
	t.Helper()
	var n int
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT count(*) FROM cron_runs`, nil, &n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMissedBlocksLaterClaim(t *testing.T) {
	db := open(t)
	addJob(t, db, "job-1", true)
	s := Store{DB: db}
	if err := s.Record(t.Context(), "job-1", slot(15), &Run{Outcome: "missed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(t.Context(), "job-1", slot(15), &Run{Outcome: "missed"}); err != nil {
		t.Fatalf("second missed = %v, want idempotent nil", err)
	}
	if got, err := s.Claim(t.Context(), "job-1", slot(15), slot(15), "pid@host"); err != nil || got {
		t.Fatalf("claim after missed = %v, %v; want false, nil", got, err)
	}
	st, err := s.State(t.Context(), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Last.Equal(slot(15)) {
		t.Fatalf("state = %+v, want last 10:15", st)
	}
}

func TestFinishStoresOutcome(t *testing.T) {
	db := open(t)
	addJob(t, db, "job-1", true)
	s := Store{DB: db}
	if ok, err := s.Claim(t.Context(), "job-1", slot(15), slot(15), "pid@host"); err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	if err := s.Finish(t.Context(), "job-1", slot(15), &Run{Outcome: "failed", Err: "boom"}); err != nil {
		t.Fatal(err)
	}
	var outcome, msg string
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT outcome, error FROM cron_runs WHERE job_id = ?1 AND scheduled_at = ?2`,
			[]any{"job-1", slot(15).UnixMilli()}, &outcome, &msg)
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "failed" || msg != "boom" {
		t.Fatalf("row = %q/%q, want failed/boom", outcome, msg)
	}
}

func TestJobsAndDisabledState(t *testing.T) {
	db := open(t)
	addJob(t, db, "job-a", true)
	addJob(t, db, "job-b", false)
	s := Store{DB: db}
	jobs, err := s.Jobs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].ID != "job-a" || jobs[1].ID != "job-b" {
		t.Fatalf("jobs = %+v, want job-a, job-b", jobs)
	}
	if !jobs[0].Enabled || jobs[1].Enabled {
		t.Fatalf("enabled = %v/%v, want true/false", jobs[0].Enabled, jobs[1].Enabled)
	}
	if jobs[0].CatchUp != "run_once" || jobs[0].MaxCatchUp != 3 ||
		jobs[0].ApprovalMode != "ask" || !jobs[0].CreatedAt.Equal(time.UnixMilli(1000)) {
		t.Fatalf("defaults not read: %+v", jobs[0])
	}
}

// TestActiveQueuedAndTake: a queued claim is visible as queued and TakeQueued
// promotes exactly that slot to running.
func TestActiveQueuedAndTake(t *testing.T) {
	db := open(t)
	addJob(t, db, "job-1", true)
	s := Store{DB: db}
	if ok, err := s.Claim(t.Context(), "job-1", slot(15), slot(15), "pid@host"); err != nil || !ok {
		t.Fatalf("running claim = %v, %v", ok, err)
	}
	if ok, err := s.Claim(t.Context(), "job-1", slot(16), time.Time{}, "pid@host"); err != nil || !ok {
		t.Fatalf("queued claim = %v, %v", ok, err)
	}
	a, err := s.Active(t.Context(), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Running != 1 || a.Queued != 1 {
		t.Fatalf("active = %+v, want 1 running 1 queued", a)
	}
	takeQueued(t, s)
}

func takeQueued(t *testing.T, s Store) {
	t.Helper()
	got, ok, err := s.TakeQueued(t.Context(), "job-1", slot(17))
	if err != nil || !ok || !got.Equal(slot(16)) {
		t.Fatalf("TakeQueued = %s, %v, %v; want 10:16", got, ok, err)
	}
	a, err := s.Active(t.Context(), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Running != 2 || a.Queued != 0 {
		t.Fatalf("active after take = %+v, want 2 running", a)
	}
	if _, ok, err = s.TakeQueued(t.Context(), "job-1", slot(18)); err != nil || ok {
		t.Fatalf("empty queue = %v, %v; want false, nil", ok, err)
	}
}

// TestFinishMonitorFields: exit/attempt go to stop_reason and the timestamps
// expose the duration (no new columns).
func TestFinishMonitorFields(t *testing.T) {
	db := open(t)
	addJob(t, db, "job-1", true)
	s := Store{DB: db}
	if ok, err := s.Claim(t.Context(), "job-1", slot(15), slot(15), "pid@host"); err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	started := slot(15)
	if err := s.Finish(t.Context(), "job-1", slot(15), &Run{
		Outcome: "failed", Err: "boom", Exit: 3, Attempt: 1,
		StartedAt: started, FinishedAt: started.Add(1500 * time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}
	var (
		reason   string
		finished int64
	)
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT stop_reason, finished_at FROM cron_runs
			WHERE job_id = ?1 AND scheduled_at = ?2`,
			[]any{"job-1", slot(15).UnixMilli()}, &reason, &finished)
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "exit=3 attempt=1 duration_ms=1500"
	if reason != want {
		t.Fatalf("stop_reason = %q, want %q", reason, want)
	}
	if finished != slot(15).Add(1500*time.Millisecond).UnixMilli() {
		t.Fatalf("finished_at = %d, want %d", finished, slot(15).Add(1500*time.Millisecond).UnixMilli())
	}
}

// TestRecordSkippedOverlapBlocksClaim: the overlap row is terminal and owns
// the slot, so a later claim loses.
func TestRecordSkippedOverlapBlocksClaim(t *testing.T) {
	db := open(t)
	addJob(t, db, "job-1", true)
	s := Store{DB: db}
	if err := s.Record(t.Context(), "job-1", slot(15), &Run{Outcome: "skipped_overlap", Err: "running"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Claim(t.Context(), "job-1", slot(15), slot(15), "pid@host"); err != nil || ok {
		t.Fatalf("claim after skipped_overlap = %v, %v; want false, nil", ok, err)
	}
}
