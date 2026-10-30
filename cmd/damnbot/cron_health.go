package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/damnbot/damnbot/internal/cron"
	"github.com/damnbot/damnbot/internal/ctxdb"
)

// cronHealthCondition is one schedule-health finding. It is derived from
// cron_jobs + cron_runs; crash-slot recovery stays with REL-09.
type cronHealthCondition struct {
	JobID     string `json:"job_id"`
	Name      string `json:"name"`
	Condition string `json:"condition"` // stalled|failing|stale_success
	Detail    string `json:"detail"`
}

type cronRunRow struct {
	Scheduled int64
	Outcome   string
}

// evaluateCronHealth derives stalled/failing/stale conditions from the stored
// schedule and the newest runs. It never writes.
func evaluateCronHealth(ctx context.Context, db *ctxdb.Store, now time.Time) ([]cronHealthCondition, error) {
	jobs, err := newCronStore(db).Jobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("cron health jobs: %w", err)
	}
	var out []cronHealthCondition
	for i := range jobs {
		j := &jobs[i]
		if !j.Enabled {
			continue
		}
		runs, err := cronRecentRuns(ctx, db, j.ID, 10)
		if err != nil {
			return nil, fmt.Errorf("cron health runs: %w", err)
		}
		out = append(out, evaluateJobHealth(j, runs, now)...)
	}
	return out, nil
}

func evaluateJobHealth(j *cron.Job, runs []cronRunRow, now time.Time) []cronHealthCondition {
	var out []cronHealthCondition
	sched, err := cron.ParseSchedule(j.Schedule, time.Local)
	if err != nil {
		return []cronHealthCondition{{j.ID, j.Name, "stalled", "invalid schedule"}}
	}
	last := j.CreatedAt
	if len(runs) > 0 && runs[0].Scheduled > 0 {
		last = time.UnixMilli(runs[0].Scheduled)
	}
	if now.After(sched.Next(last).Add(10 * time.Minute)) {
		out = append(out, cronHealthCondition{j.ID, j.Name, "stalled", "no run since " + last.UTC().Format(time.RFC3339)})
	}
	failed, lastOK := 0, int64(0)
	for _, r := range runs {
		if r.Outcome == "ok" {
			if r.Scheduled > lastOK {
				lastOK = r.Scheduled
			}
			break
		}
		if r.Outcome == "failed" {
			failed++
		}
	}
	if failed >= 3 {
		out = append(out, cronHealthCondition{j.ID, j.Name, "failing", fmt.Sprintf("%d consecutive failures", failed)})
	}
	if lastOK == 0 || now.Sub(time.UnixMilli(lastOK)) > 24*time.Hour {
		out = append(out, cronHealthCondition{j.ID, j.Name, "stale_success", "no successful run in 24h"})
	}
	return out
}

func cronRecentRuns(ctx context.Context, db *ctxdb.Store, jobID string, limit int) ([]cronRunRow, error) {
	out := make([]cronRunRow, 0, limit)
	err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		var err error
		out, err = queryCronRuns(tx, jobID, limit)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("cron health runs: %w", err)
	}
	return out, nil
}

func queryCronRuns(tx *ctxdb.Tx, jobID string, limit int) ([]cronRunRow, error) {
	rows, err := tx.Strings(`SELECT CAST(scheduled_at AS TEXT) || char(31) || coalesce(outcome, '')
		FROM cron_runs WHERE job_id = ?1 ORDER BY scheduled_at DESC LIMIT ?2`, jobID, limit)
	if err != nil {
		return nil, fmt.Errorf("cron health runs: %w", err)
	}
	out := make([]cronRunRow, 0, len(rows))
	for _, row := range rows {
		at, outcome, _ := cut31(row)
		var ms int64
		if _, serr := fmt.Sscanf(at, "%d", &ms); serr != nil {
			continue
		}
		out = append(out, cronRunRow{Scheduled: ms, Outcome: outcome})
	}
	return out, nil
}

func cut31(s string) (string, string, bool) {
	for i := range len(s) {
		if s[i] == 0x1f {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

// syncCronHealth writes one module_state marker per condition and sends at most
// one notice per transition; recovered conditions get one recovery notice.
func syncCronHealth(ctx context.Context, db *ctxdb.Store, now time.Time, notify func(context.Context, string) error) error {
	if cerr := ctx.Err(); cerr != nil {
		return fmt.Errorf("cron health: %w", cerr)
	}
	conds, err := evaluateCronHealth(ctx, db, now)
	if err != nil {
		return err
	}
	active := map[string]cronHealthCondition{}
	for _, c := range conds {
		active[cronHealthKey(c)] = c
	}
	if cerr := ctx.Err(); cerr != nil {
		return fmt.Errorf("cron health: %w", cerr)
	}
	existing, err := cronHealthMarkers(ctx, db)
	if err != nil {
		return err
	}
	if err := announceCronHealth(ctx, db, active, existing, now, notify); err != nil {
		return err
	}
	return recoverCronHealth(ctx, db, active, existing, notify)
}

func cronHealthMarkers(ctx context.Context, db *ctxdb.Store) ([]string, error) {
	var keys []string
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		var err error
		keys, err = tx.Strings(`SELECT key FROM module_state WHERE module_id = 'gateway' AND key LIKE 'cronhealth:%'`)
		if err != nil {
			return fmt.Errorf("cron health markers: %w", err)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("cron health markers: %w", err)
	}
	return keys, nil
}

func announceCronHealth(ctx context.Context, db *ctxdb.Store, active map[string]cronHealthCondition, existing []string, now time.Time, notify func(context.Context, string) error) error {
	for key, c := range active {
		if slices.Contains(existing, key) {
			continue
		}
		if err := markCronHealth(ctx, db, key, c, now); err != nil {
			return err
		}
		if notify != nil {
			if err := notify(ctx, "cron health: "+c.Name+" "+c.Condition+" ("+c.Detail+")"); err != nil {
				return err
			}
		}
	}
	return nil
}

func recoverCronHealth(ctx context.Context, db *ctxdb.Store, active map[string]cronHealthCondition, existing []string, notify func(context.Context, string) error) error {
	for _, key := range existing {
		if _, ok := active[key]; ok {
			continue
		}
		if err := clearCronHealth(ctx, db, key); err != nil {
			return err
		}
		if notify != nil {
			if err := notify(ctx, "cron health recovered: "+key); err != nil {
				return err
			}
		}
	}
	return nil
}

func markCronHealth(ctx context.Context, db *ctxdb.Store, key string, c cronHealthCondition, now time.Time) error {
	body, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("cron health encode: %w", err)
	}
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at) VALUES ('gateway', ?1, ?2, ?3)`,
			key, string(body), now.UnixMilli())
		if xerr != nil {
			return fmt.Errorf("cron health marker: %w", xerr)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("cron health marker: %w", err)
	}
	return nil
}

func clearCronHealth(ctx context.Context, db *ctxdb.Store, key string) error {
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`DELETE FROM module_state WHERE module_id = 'gateway' AND key = ?1`, key)
		if xerr != nil {
			return fmt.Errorf("cron health recovery: %w", xerr)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("cron health recovery: %w", err)
	}
	return nil
}

func cronHealthKey(c cronHealthCondition) string {
	return "cronhealth:" + c.JobID + ":" + c.Condition
}
