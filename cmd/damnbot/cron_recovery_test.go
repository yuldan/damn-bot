package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func recoveryDB(t *testing.T) *ctxdb.Store {
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

func seedCronJob(t *testing.T, db *ctxdb.Store, id, name string, pauseAfter int) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		if _, xerr := tx.Exec(`INSERT INTO cron_jobs(id, name, owner_identity, schedule, prompt, prompt_sha,
				cwd, pause_after_crashes, enabled, created_at, updated_at)
			VALUES (?1, ?2, 'serve', '@daily', 'p', ?3, '/tmp', ?4, 1, ?5, ?5)`,
			id, name, "sha-"+id, pauseAfter, now); xerr != nil {
			return fmt.Errorf("seed job: %w", xerr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func seedOpenRun(t *testing.T, db *ctxdb.Store, job string, at time.Time) {
	t.Helper()
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		if _, xerr := tx.Exec(`INSERT INTO cron_runs(job_id, scheduled_at, claimed_by, started_at)
			VALUES (?1, ?2, 'serve', ?2)`, job, at.UnixMilli()); xerr != nil {
			return fmt.Errorf("seed run: %w", xerr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestRecoverCrashedRunsClosesSlotOnce is REL-09: an open slot from a dead
// process becomes crashed, the owner is notified exactly once, and a second
// recovery (restart) writes nothing. The negative control is the second call.
func TestRecoverCrashedRunsClosesSlotOnce(t *testing.T) {
	db := recoveryDB(t)
	seedCronJob(t, db, "job1", "harian", 3)
	at := time.UnixMilli(1_700_000_000_000)
	seedOpenRun(t, db, "job1", at)

	var (
		mu      sync.Mutex
		notices []string
	)
	notify := func(_ context.Context, key, _ string) {
		mu.Lock()
		notices = append(notices, key)
		mu.Unlock()
	}
	n, err := recoverCrashedRuns(t.Context(), db, notify)
	if err != nil || n != 1 {
		t.Fatalf("recover = %d, %v; want 1 crashed slot", n, err)
	}
	var outcome string
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT outcome FROM cron_runs WHERE job_id = 'job1'`, nil, &outcome)
	}); err != nil {
		t.Fatal(err)
	}
	if outcome != "crashed" {
		t.Fatalf("outcome = %q, want crashed", outcome)
	}
	mu.Lock()
	first := len(notices)
	mu.Unlock()
	if first != 1 {
		t.Fatalf("notices = %d, want 1 per slot", first)
	}
	if n, err := recoverCrashedRuns(t.Context(), db, notify); err != nil || n != 0 {
		t.Fatalf("second recovery = %d, %v; want 0", n, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notices) != first {
		t.Fatalf("second recovery notified again: %v", notices)
	}
}

// TestRecoverPausesAfterConsecutiveCrashes: three crashes pause the job and
// notify once; two crashes (negative control) do not pause.
func TestRecoverPausesAfterConsecutiveCrashes(t *testing.T) {
	db := recoveryDB(t)
	seedCronJob(t, db, "job3", "tiga", 3)
	seedCronJob(t, db, "job2", "dua", 3)
	base := time.UnixMilli(1_700_000_000_000)
	for i := range 3 {
		seedOpenRun(t, db, "job3", base.Add(time.Duration(i)*time.Hour))
	}
	for i := range 2 {
		seedOpenRun(t, db, "job2", base.Add(time.Duration(i)*time.Hour))
	}
	var pauseNotes []string
	notify := func(_ context.Context, key, _ string) {
		if strings.HasPrefix(key, "cron-pause:") {
			pauseNotes = append(pauseNotes, key)
		}
	}
	if _, err := recoverCrashedRuns(t.Context(), db, notify); err != nil {
		t.Fatal(err)
	}
	var enabled, pausedAt int64
	var reason string
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT enabled, coalesce(paused_at, 0), coalesce(pause_reason, '') FROM cron_jobs WHERE id = 'job3'`,
			nil, &enabled, &pausedAt, &reason)
	}); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 || pausedAt == 0 || !strings.Contains(reason, "crashed 3") {
		t.Fatalf("job3 enabled=%d paused_at=%d reason=%q, want paused after 3 crashes", enabled, pausedAt, reason)
	}
	var enabled2 int64
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT enabled FROM cron_jobs WHERE id = 'job2'`, nil, &enabled2)
	}); err != nil {
		t.Fatal(err)
	}
	if enabled2 != 1 {
		t.Fatalf("job2 enabled=%d, want still enabled after only 2 crashes", enabled2)
	}
	if len(pauseNotes) != 1 {
		t.Fatalf("pause notices = %v, want exactly one", pauseNotes)
	}
	// A restart must not repeat the pause notice.
	if _, err := recoverCrashedRuns(t.Context(), db, notify); err != nil {
		t.Fatal(err)
	}
	if len(pauseNotes) != 1 {
		t.Fatalf("pause notices after restart = %v, want one", pauseNotes)
	}
}
