// Package cronstore is the ctxdb storage of the F4 scheduler (SPEC §9.8,
// §11.5; ADR-074, ADR-248). It uses cron_jobs and cron_runs from migration
// 0004 (no DDL): UNIQUE(job_id, scheduled_at) is the slot idempotency key.
// Local value types only; cmd converts when `serve` wires the scheduler.
package cronstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// Store is cron storage over one ctxdb store.
type Store struct{ DB *ctxdb.Store }

// Job is one cron_jobs row (script jobs are caller-supplied).
type Job struct {
	ID, Name, Schedule, TZ, Cwd  string
	Owner, CatchUp, ApprovalMode string
	Overlap                      string
	MaxCatchUp                   int
	Enabled                      bool
	CreatedAt                    time.Time
}

// State is the newest recorded slot.
type State struct{ Last time.Time }

// Active is the open-slot census: started rows run, unstarted rows queue.
type Active struct{ Running, Queued int }

// Run is the outcome and monitor record stored on a claimed slot.
type Run struct {
	Outcome, Err          string
	Exit, Attempt         int
	StartedAt, FinishedAt time.Time
}

// Jobs lists every job definition; the scheduler filters by Enabled.
func (s Store) Jobs(ctx context.Context) ([]Job, error) {
	var out []Job
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		ids, err := tx.Strings(`SELECT id FROM cron_jobs ORDER BY name`)
		if err != nil {
			return fmt.Errorf("list ids: %w", err)
		}
		for _, id := range ids {
			var (
				j       Job
				enabled int64
				created int64
			)
			err := tx.Get(`SELECT id, name, schedule, coalesce(tz, ''), cwd, approval_mode,
					catch_up, max_catch_up, overlap, owner_identity, enabled, created_at
				FROM cron_jobs WHERE id = ?1`, []any{id},
				&j.ID, &j.Name, &j.Schedule, &j.TZ, &j.Cwd, &j.ApprovalMode,
				&j.CatchUp, &j.MaxCatchUp, &j.Overlap, &j.Owner, &enabled, &created)
			if err != nil {
				return fmt.Errorf("read job %s: %w", id, err)
			}
			j.Enabled, j.CreatedAt = enabled != 0, time.UnixMilli(created)
			out = append(out, j)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cronstore jobs: %w", err)
	}
	return out, nil
}

// State returns the catch-up watermark (newest scheduled_at).
func (s Store) State(ctx context.Context, jobID string) (State, error) {
	var last int64
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		if err := tx.Get(`SELECT coalesce(max(scheduled_at), 0) FROM cron_runs WHERE job_id = ?1`,
			[]any{jobID}, &last); err != nil {
			return fmt.Errorf("watermark: %w", err)
		}
		return nil
	})
	if err != nil {
		return State{}, fmt.Errorf("cronstore state %s: %w", jobID, err)
	}
	if last == 0 {
		return State{}, nil
	}
	return State{Last: time.UnixMilli(last)}, nil
}

// Active counts open slots: started rows are running, unstarted rows queued.
func (s Store) Active(ctx context.Context, jobID string) (Active, error) {
	var a Active
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT coalesce(sum(started_at IS NOT NULL), 0),
				coalesce(sum(started_at IS NULL), 0)
			FROM cron_runs WHERE job_id = ?1 AND outcome IS NULL`, []any{jobID}, &a.Running, &a.Queued)
	})
	if err != nil {
		return Active{}, fmt.Errorf("cronstore active %s: %w", jobID, err)
	}
	return a, nil
}

// Claim inserts the slot; a duplicate loses the claim. A zero started queues
// the slot instead of starting it (overlap queue_one).
func (s Store) Claim(ctx context.Context, jobID string, at, started time.Time, owner string) (bool, error) {
	var start any
	if !started.IsZero() {
		start = started.UnixMilli()
	}
	owned := false
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		n, err := tx.Exec(`INSERT INTO cron_runs(job_id, scheduled_at, claimed_by, started_at)
			VALUES (?1, ?2, ?3, ?4) ON CONFLICT(job_id, scheduled_at) DO NOTHING`,
			jobID, at.UnixMilli(), owner, start)
		if err != nil {
			return fmt.Errorf("insert slot: %w", err)
		}
		owned = n == 1
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("cronstore claim %s@%s: %w", jobID, at.Format(time.RFC3339), err)
	}
	return owned, nil
}

// TakeQueued starts the oldest queued slot and returns its scheduled time.
func (s Store) TakeQueued(ctx context.Context, jobID string, started time.Time) (time.Time, bool, error) {
	var (
		at  int64
		got bool
	)
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		err := tx.Get(`SELECT scheduled_at FROM cron_runs
			WHERE job_id = ?1 AND outcome IS NULL AND started_at IS NULL
			ORDER BY scheduled_at LIMIT 1`, []any{jobID}, &at)
		if errors.Is(err, ctxdb.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("queued slot: %w", err)
		}
		n, err := tx.Exec(`UPDATE cron_runs SET started_at = ?3
			WHERE job_id = ?1 AND scheduled_at = ?2 AND started_at IS NULL`,
			jobID, at, started.UnixMilli())
		if err != nil {
			return fmt.Errorf("start queued: %w", err)
		}
		got = n == 1
		return nil
	})
	if err != nil {
		return time.Time{}, false, fmt.Errorf("cronstore queued %s: %w", jobID, err)
	}
	if !got {
		return time.Time{}, false, nil
	}
	return time.UnixMilli(at), true, nil
}

// Record writes a terminal row for an unclaimed slot (missed/skipped_overlap).
func (s Store) Record(ctx context.Context, jobID string, at time.Time, run *Run) error {
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		_, err := tx.Exec(`INSERT INTO cron_runs(job_id, scheduled_at, outcome, error)
			VALUES (?1, ?2, ?3, ?4) ON CONFLICT(job_id, scheduled_at) DO NOTHING`,
			jobID, at.UnixMilli(), run.Outcome, run.Err)
		if err != nil {
			return fmt.Errorf("insert terminal slot: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("cronstore record %s@%s: %w", jobID, at.Format(time.RFC3339), err)
	}
	return nil
}

// Finish closes a claimed slot with its outcome and monitor fields. Duration
// comes from started_at/finished_at; exit and attempt have no column and go to
// stop_reason (reported in the PR, no DDL).
func (s Store) Finish(ctx context.Context, jobID string, at time.Time, run *Run) error {
	finished := run.FinishedAt
	if finished.IsZero() {
		finished = time.Now()
	}
	attempt := max(run.Attempt, 1)
	reason := fmt.Sprintf("exit=%d attempt=%d duration_ms=%d",
		run.Exit, attempt, finished.Sub(run.StartedAt).Milliseconds())
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		n, err := tx.Exec(`UPDATE cron_runs SET finished_at = ?3, stop_reason = ?4,
				outcome = ?5, error = ?6
			WHERE job_id = ?1 AND scheduled_at = ?2`,
			jobID, at.UnixMilli(), finished.UnixMilli(), reason, run.Outcome, run.Err)
		if err != nil {
			return fmt.Errorf("update slot: %w", err)
		}
		if n != 1 {
			return errors.New("slot not claimed")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("cronstore finish %s@%s: %w", jobID, at.Format(time.RFC3339), err)
	}
	return nil
}
