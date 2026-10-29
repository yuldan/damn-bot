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
	ID, Name, Schedule, TZ, Cwd string
	CatchUp, ApprovalMode       string
	MaxCatchUp                  int
	Enabled                     bool
	CreatedAt                   time.Time
}

// State is the newest recorded slot.
type State struct{ Last time.Time }

// Run is the outcome stored on a claimed slot.
type Run struct{ Outcome, Err string }

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
					catch_up, max_catch_up, enabled, created_at
				FROM cron_jobs WHERE id = ?1`, []any{id},
				&j.ID, &j.Name, &j.Schedule, &j.TZ, &j.Cwd, &j.ApprovalMode,
				&j.CatchUp, &j.MaxCatchUp, &enabled, &created)
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

// Claim inserts the slot row; a duplicate loses the claim.
func (s Store) Claim(ctx context.Context, jobID string, at time.Time, owner string) (bool, error) {
	owned := false
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		n, err := tx.Exec(`INSERT INTO cron_runs(job_id, scheduled_at, claimed_by, started_at)
			VALUES (?1, ?2, ?3, ?4) ON CONFLICT(job_id, scheduled_at) DO NOTHING`,
			jobID, at.UnixMilli(), owner, time.Now().UnixMilli())
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

// Missed records a slot the policy did not run so it cannot be claimed later.
func (s Store) Missed(ctx context.Context, jobID string, at time.Time) error {
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		_, err := tx.Exec(`INSERT INTO cron_runs(job_id, scheduled_at, outcome)
			VALUES (?1, ?2, 'missed') ON CONFLICT(job_id, scheduled_at) DO NOTHING`, jobID, at.UnixMilli())
		if err != nil {
			return fmt.Errorf("insert missed: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("cronstore missed %s@%s: %w", jobID, at.Format(time.RFC3339), err)
	}
	return nil
}

// Finish closes a claimed slot with its outcome.
func (s Store) Finish(ctx context.Context, jobID string, at time.Time, run Run) error {
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		n, err := tx.Exec(`UPDATE cron_runs SET finished_at = ?3, outcome = ?4, error = ?5
			WHERE job_id = ?1 AND scheduled_at = ?2`,
			jobID, at.UnixMilli(), time.Now().UnixMilli(), run.Outcome, run.Err)
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
