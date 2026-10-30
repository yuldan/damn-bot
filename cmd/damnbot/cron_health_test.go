package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/cron"
	"github.com/damnbot/damnbot/internal/ctxdb"
)

func healthDB(t *testing.T) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	return db
}

func healthJob(t *testing.T, db *ctxdb.Store, name, schedule string, created time.Time) string {
	t.Helper()
	j, _, err := newCronStore(db).Create(context.Background(), &cron.Job{
		Name: name, Schedule: schedule, Prompt: name, Cwd: t.TempDir(), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`UPDATE cron_jobs SET created_at = ?2 WHERE id = ?1`, j.ID, created.UnixMilli())
		if xerr != nil {
			return fmt.Errorf("update created: %w", xerr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return j.ID
}

func healthRun(t *testing.T, db *ctxdb.Store, jobID string, at time.Time, outcome string) {
	t.Helper()
	if err := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO cron_runs(job_id, scheduled_at, outcome, error) VALUES (?1, ?2, ?3, '')`,
			jobID, at.UnixMilli(), outcome)
		if xerr != nil {
			return fmt.Errorf("insert run: %w", xerr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestCronHealthConditions: stalled, repeated failure, healthy and stale
// success are derived from cron_runs; healthy jobs produce nothing.
func TestCronHealthConditions(t *testing.T) {
	db := healthDB(t)
	now := time.Now()
	stalled := healthJob(t, db, "stalled", "* * * * *", now.Add(-2*time.Hour))
	failing := healthJob(t, db, "failing", "0 * * * *", now)
	healthy := healthJob(t, db, "healthy", "* * * * *", now)
	for i := range 3 {
		healthRun(t, db, failing, now.Add(-time.Duration(i+1)*time.Minute), "failed")
	}
	healthRun(t, db, healthy, now.Add(-time.Minute), "ok")

	conds, err := evaluateCronHealth(context.Background(), db, now)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, c := range conds {
		found[c.JobID+":"+c.Condition] = true
	}
	if !found[stalled+":stalled"] || !found[failing+":failing"] || !found[failing+":stale_success"] {
		t.Fatalf("conditions = %+v", conds)
	}
	for _, c := range conds {
		if c.JobID == healthy {
			t.Fatalf("healthy job produced %+v", c)
		}
	}
}

// TestCronHealthNoticesOnceAndRecovery: one notice per transition, no spam,
// and one recovery notice when the job becomes healthy.
func TestCronHealthNoticesOnceAndRecovery(t *testing.T) {
	db := healthDB(t)
	now := time.Now()
	job := healthJob(t, db, "stalled", "* * * * *", now.Add(-2*time.Hour))
	var notices []string
	notify := func(_ context.Context, body string) error {
		notices = append(notices, body)
		return nil
	}
	if err := syncCronHealth(context.Background(), db, now, notify); err != nil {
		t.Fatal(err)
	}
	first := len(notices)
	if first == 0 {
		t.Fatal("no health notice")
	}
	if err := syncCronHealth(context.Background(), db, now, notify); err != nil {
		t.Fatal(err)
	}
	if len(notices) != first {
		t.Fatalf("notices repeated: %d -> %d", first, len(notices))
	}
	healthRun(t, db, job, now, "ok")
	if err := syncCronHealth(context.Background(), db, now.Add(time.Minute), notify); err != nil {
		t.Fatal(err)
	}
	recovered := 0
	for _, n := range notices[first:] {
		if strings.Contains(n, "recovered") {
			recovered++
		}
	}
	if recovered == 0 || len(notices) != first+recovered {
		t.Fatalf("recovery notices = %v", notices)
	}
}
