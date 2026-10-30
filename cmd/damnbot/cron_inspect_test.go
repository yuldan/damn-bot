package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/cron"
	"github.com/damnbot/damnbot/internal/ctxdb"
)

func inspectDB(t *testing.T) *ctxdb.Store {
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

func TestCronNotepadRoundTrip(t *testing.T) {
	db := inspectDB(t)
	ctx := context.Background()
	if _, found, err := notepadGet(ctx, db, "j1"); err != nil || found {
		t.Fatalf("empty notepad = %v %v", found, err)
	}
	if err := notepadSet(ctx, db, "j1", "hello"); err != nil {
		t.Fatal(err)
	}
	if got, found, err := notepadGet(ctx, db, "j1"); err != nil || !found || got != "hello" {
		t.Fatalf("get = %q %v %v", got, found, err)
	}
	if err := notepadSet(ctx, db, "j1", strings.Repeat("x", 4097)); err == nil {
		t.Fatal("oversize notepad accepted")
	}
	if err := notepadClear(ctx, db, "j1"); err != nil {
		t.Fatal(err)
	}
	if _, found, gerr := notepadGet(ctx, db, "j1"); gerr != nil || found {
		t.Fatal("notepad not cleared")
	}
}

func TestCronRunsQueryLimitAndOrder(t *testing.T) {
	db := inspectDB(t)
	ctx := context.Background()
	j, _, err := newCronStore(db).Create(ctx, &cron.Job{Name: "j1", Schedule: "0 * * * *", Prompt: "p", Cwd: t.TempDir(), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		if werr := db.Write(ctx, func(tx *ctxdb.Tx) error {
			_, xerr := tx.Exec(`INSERT INTO cron_runs(job_id, scheduled_at, outcome, error) VALUES (?1, ?2, 'ok', '')`, j.ID, time.Now().Add(-time.Duration(i)*time.Minute).UnixMilli())
			if xerr != nil {
				return fmt.Errorf("insert run: %w", xerr)
			}
			return nil
		}); werr != nil {
			t.Fatal(werr)
		}
	}
	runs, err := cronRuns(ctx, db, j.ID, 2, false)
	if err != nil || len(runs) != 2 || runs[0].ScheduledAt < runs[1].ScheduledAt {
		t.Fatalf("runs = %+v err=%v", runs, err)
	}
	if many, qerr := cronRuns(ctx, db, j.ID, 999, false); qerr != nil || len(many) != 5 {
		t.Fatalf("limit cap returned %d", len(many))
	}
}

func TestCronDoctorFindsBrokenConfig(t *testing.T) {
	db := inspectDB(t)
	ctx := context.Background()
	j, _, err := newCronStore(db).Create(ctx, &cron.Job{Name: "bad", Schedule: "* * * * *", Prompt: "p", Cwd: t.TempDir(), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if uerr := db.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`UPDATE cron_jobs SET schedule = 'bad' WHERE id = ?1`, j.ID)
		if xerr != nil {
			return fmt.Errorf("update schedule: %w", xerr)
		}
		return nil
	}); uerr != nil {
		t.Fatal(uerr)
	}
	findings, err := cronDoctor(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(findings, "\n")
	for _, want := range []string{"invalid schedule"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("doctor findings %q lack %q", joined, want)
		}
	}
}

type countExec struct{ n atomic.Int32 }

func (e *countExec) Run(context.Context, *cron.Job) (string, error) {
	e.n.Add(1)
	return "ok", nil
}

func TestCronTickParallelClaimsOnce(t *testing.T) {
	db := inspectDB(t)
	ctx := context.Background()
	store := newCronStore(db)
	due, _, err := store.Create(ctx, &cron.Job{Name: "due", Schedule: "* * * * *", Prompt: "p", Cwd: t.TempDir(), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if uerr := db.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`UPDATE cron_jobs SET created_at = ?2 WHERE id = ?1`, due.ID, time.Now().Add(-2*time.Hour).UnixMilli())
		if xerr != nil {
			return fmt.Errorf("update created: %w", xerr)
		}
		return nil
	}); uerr != nil {
		t.Fatal(uerr)
	}
	exec := &countExec{}
	clock := wallClock{}
	s := &cron.Scheduler{Store: store, Executor: exec, Clock: clock, Owner: "cli", Incidents: &cronIncidents{db: db}}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if terr := s.Tick(ctx); terr != nil {
				t.Errorf("tick: %v", terr)
			}
		})
	}
	wg.Wait()
	if got := exec.n.Load(); got != 1 {
		t.Fatalf("parallel tick ran %d jobs, want 1", got)
	}
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`UPDATE cron_jobs SET enabled = 0`)
		if xerr != nil {
			return fmt.Errorf("disable jobs: %w", xerr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := exec.n.Load(); got != 1 {
		t.Fatalf("no-enabled tick ran %d jobs, want 1 total", got)
	}
}
