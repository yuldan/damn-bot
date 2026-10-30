package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/cron"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/cronstore"
)

// The cron_jobs CRUD lives here because `MODULE cronstore` is capped at 200
// LOC: cronstore keeps the scheduler reads and slot claims, while the
// composition root owns the job-definition writes (SPEC §9.8, ADR-252).
var errCronJobNotFound = errors.New("cron job not found")

// cronStore is the one cron.AdminStore of the CLI, cronjob_manage and /cron.
type cronStore struct {
	cronstore.Store
	db *ctxdb.Store
}

func newCronStore(db *ctxdb.Store) *cronStore {
	return &cronStore{Store: cronstore.Store{DB: db}, db: db}
}

func (s *cronStore) Jobs(ctx context.Context) ([]cron.Job, error) {
	rows, err := s.Store.Jobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("cron jobs: %w", err)
	}
	out := make([]cron.Job, len(rows))
	for i := range rows {
		out[i] = fromStore(&rows[i])
	}
	return out, nil
}

func fromStore(r *cronstore.Job) cron.Job {
	return cron.Job{
		ID: r.ID, Name: r.Name, Schedule: r.Schedule, TZ: r.TZ, Prompt: r.Prompt, Owner: r.Owner,
		Enabled: r.Enabled, CatchUp: r.CatchUp, ApprovalMode: r.ApprovalMode, MaxCatchUp: r.MaxCatchUp,
		Overlap: r.Overlap, Cwd: r.Cwd, Gate: r.Gate, CreatedAt: r.CreatedAt,
	}
}

// Create inserts j; an identical normalized prompt+schedule returns the
// existing row with created=false (COST-14 dedup).
func (s *cronStore) Create(ctx context.Context, j *cron.Job) (cron.Job, bool, error) {
	now := time.Now().UnixMilli()
	if j.ID == "" {
		j.ID = rand.Text()
	}
	enabled, sha := 0, promptSHA(j.Prompt)
	if j.Enabled {
		enabled = 1
	}
	var (
		old                cronstore.Job
		rowEnabled, rowNew int64
		found              = true
		n                  int64
	)
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		err := tx.Get(`SELECT id, name, schedule, coalesce(tz, ''), cwd, prompt, approval_mode,
				catch_up, max_catch_up, overlap, owner_identity, coalesce(gate, ''), enabled, created_at
			FROM cron_jobs WHERE prompt_sha = ?1 AND schedule = ?2 AND deliver_to = '{}'`,
			[]any{sha, j.Schedule}, &old.ID, &old.Name, &old.Schedule, &old.TZ, &old.Cwd, &old.Prompt,
			&old.ApprovalMode, &old.CatchUp, &old.MaxCatchUp, &old.Overlap, &old.Owner, &old.Gate, &rowEnabled, &rowNew)
		if err == nil {
			old.Enabled, old.CreatedAt = rowEnabled != 0, time.UnixMilli(rowNew)
			return nil
		}
		found = false
		n, err = tx.Exec(`INSERT INTO cron_jobs(id, name, owner_identity, schedule, tz, prompt,
				prompt_sha, cwd, gate, enabled, created_at, updated_at)
			VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?11)`,
			j.ID, j.Name, cmp.Or(j.Owner, "local"), j.Schedule, nullText(j.TZ), j.Prompt, sha, j.Cwd,
			nullText(j.Gate), enabled, now)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return fmt.Errorf("cron job %q already exists", j.Name)
			}
			return fmt.Errorf("insert job: %w", err)
		}
		return nil
	})
	if err != nil {
		return cron.Job{}, false, fmt.Errorf("cronstore create: %w", err)
	}
	if found {
		return fromStore(&old), false, nil
	}
	if n != 1 {
		return cron.Job{}, false, errors.New("cronstore create: no row inserted")
	}
	j.CreatedAt = time.UnixMilli(now)
	return *j, true, nil
}

// Update rewrites the mutable definition of one job.
func (s *cronStore) Update(ctx context.Context, j *cron.Job) error {
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		n, err := tx.Exec(`UPDATE cron_jobs SET name = ?2, schedule = ?3, tz = ?4,
				prompt = ?5, prompt_sha = ?6, cwd = ?7, gate = ?8, updated_at = ?9 WHERE id = ?1`,
			j.ID, j.Name, j.Schedule, nullText(j.TZ), j.Prompt, promptSHA(j.Prompt), j.Cwd,
			nullText(j.Gate), time.Now().UnixMilli())
		if err != nil {
			return fmt.Errorf("update job: %w", err)
		}
		if n != 1 {
			return errCronJobNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("cron update: %w", err)
	}
	return nil
}

// SetEnabled pauses (recording paused_at/reason) or resumes one job.
func (s *cronStore) SetEnabled(ctx context.Context, id string, enabled bool, reason string) error {
	now := time.Now().UnixMilli()
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		var (
			n   int64
			err error
		)
		if enabled {
			n, err = tx.Exec(`UPDATE cron_jobs SET enabled = 1, paused_at = NULL, pause_reason = NULL,
				updated_at = ?2 WHERE id = ?1`, id, now)
		} else {
			n, err = tx.Exec(`UPDATE cron_jobs SET enabled = 0, paused_at = ?2, pause_reason = ?3,
				updated_at = ?2 WHERE id = ?1`, id, now, reason)
		}
		if err != nil {
			return fmt.Errorf("set enabled: %w", err)
		}
		if n != 1 {
			return errCronJobNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("cron set enabled: %w", err)
	}
	return nil
}

// Remove deletes a job and its run history (cron_runs has no cascade).
func (s *cronStore) Remove(ctx context.Context, id string) error {
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, err := tx.Exec(`DELETE FROM cron_runs WHERE job_id = ?1`, id); err != nil {
			return fmt.Errorf("delete runs: %w", err)
		}
		n, err := tx.Exec(`DELETE FROM cron_jobs WHERE id = ?1`, id)
		if err != nil {
			return fmt.Errorf("delete job: %w", err)
		}
		if n != 1 {
			return errCronJobNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("cron remove: %w", err)
	}
	return nil
}

func (s *cronStore) State(ctx context.Context, id string) (cron.State, error) {
	st, err := s.Store.State(ctx, id)
	if err != nil {
		return cron.State{}, fmt.Errorf("cron state: %w", err)
	}
	return cron.State{Last: st.Last}, nil
}

func (s *cronStore) Active(ctx context.Context, id string) (cron.Active, error) {
	a, err := s.Store.Active(ctx, id)
	if err != nil {
		return cron.Active{}, fmt.Errorf("cron active: %w", err)
	}
	return cron.Active{Running: a.Running, Queued: a.Queued}, nil
}

func (s *cronStore) Record(ctx context.Context, id string, at time.Time, run *cron.Run) error {
	err := s.Store.Record(ctx, id, at, &cronstore.Run{
		Outcome: run.Outcome, Err: run.Err, Exit: run.Exit,
		Attempt: run.Attempt, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
	})
	if err != nil {
		return fmt.Errorf("cron record: %w", err)
	}
	return nil
}

func (s *cronStore) Finish(ctx context.Context, id string, at time.Time, run *cron.Run) error {
	err := s.Store.Finish(ctx, id, at, &cronstore.Run{
		Outcome: run.Outcome, Err: run.Err, Exit: run.Exit,
		Attempt: run.Attempt, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
	})
	if err != nil {
		return fmt.Errorf("cron finish: %w", err)
	}
	return nil
}

// ActiveJobs counts enabled jobs of one owner (COST-14 quota).
func (s *cronStore) ActiveJobs(ctx context.Context, owner string) (int, error) {
	owner = cmp.Or(owner, "local")
	n, err := s.db.Count(ctx, `SELECT COUNT(*) FROM cron_jobs WHERE enabled = 1 AND owner_identity = ?1`, owner)
	return int(n), err
}

// GatedRuns counts slots a gate prevented from reaching the executor.
func (s *cronStore) GatedRuns(ctx context.Context) (int64, error) {
	return s.db.Count(ctx, `SELECT COUNT(*) FROM cron_runs WHERE outcome = 'gated'`)
}

// GateHash reads the last normalized monitor hash of one job.
func (s *cronStore) GateHash(ctx context.Context, jobID string) (string, bool, error) {
	var raw string
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		gerr := tx.Get(`SELECT value FROM module_state WHERE module_id = 'cron' AND key = ?1`,
			[]any{"gate_hash:" + jobID}, &raw)
		if errors.Is(gerr, ctxdb.ErrNotFound) {
			return nil
		}
		return gerr
	})
	if err != nil {
		return "", false, fmt.Errorf("cron gate hash: %w", err)
	}
	return raw, raw != "", nil
}

// SetGateHash stores the last normalized monitor hash of one job.
func (s *cronStore) SetGateHash(ctx context.Context, jobID, hash string) error {
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at)
			VALUES ('cron', ?1, ?2, ?3)
			ON CONFLICT(module_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			"gate_hash:"+jobID, hash, time.Now().UnixMilli())
		return xerr
	})
	if err != nil {
		return fmt.Errorf("cron save gate hash: %w", err)
	}
	return nil
}

func promptSHA(prompt string) string {
	sum := sha256.Sum256([]byte(strings.Join(strings.Fields(prompt), " ")))
	return hex.EncodeToString(sum[:])
}

func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}
