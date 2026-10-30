package main

import (
	"context"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// cronNotice is one owner notice produced by crash recovery. Notices are
// collected inside the write transaction and enqueued after it commits, so the
// enqueue never nests a second writer.
type cronNotice struct{ key, body string }

// recoverCrashedRuns closes every cron slot a previous process left open and
// applies REL-09: the crashed slot is not retried, the job keeps its next slot,
// the owner is notified once per slot, and only pause_after_crashes consecutive
// crashes auto-pause the job (with one notice and `damnbot cron resume`).
func recoverCrashedRuns(ctx context.Context, db *ctxdb.Store, notify func(context.Context, string, string)) (int, error) {
	if db == nil {
		return 0, nil
	}
	now := time.Now().UnixMilli()
	var (
		closed  int
		notices []cronNotice
	)
	err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		rows, xerr := tx.Ints(`SELECT rowid FROM cron_runs
			WHERE started_at IS NOT NULL AND outcome IS NULL ORDER BY scheduled_at, id`)
		if xerr != nil {
			return fmt.Errorf("crashed runs: %w", xerr)
		}
		for _, row := range rows {
			out, xerr := closeOpenRun(tx, row, now)
			if xerr != nil {
				return xerr
			}
			if !out.handled {
				continue
			}
			closed++
			notices = append(notices, out.notice)
		}
		paused, err := pauseCrashedJobs(tx, now)
		if err != nil {
			return err
		}
		notices = append(notices, paused...)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("gateway: recover cron runs: %w", err)
	}
	if notify != nil {
		for _, n := range notices {
			notify(ctx, n.key, n.body)
		}
	}
	return closed, nil
}

// crashClose is the result of closing one open run.
type crashClose struct {
	handled bool
	notice  cronNotice
}

// closeOpenRun marks one orphaned slot crashed, or requeues it when the job
// opted into retry_on_crash/idempotent.
func closeOpenRun(tx *ctxdb.Tx, row, now int64) (crashClose, error) {
	var (
		at          int64
		job, name   string
		retry, idem int64
	)
	if err := tx.Get(`SELECT r.job_id, j.name, r.scheduled_at, j.retry_on_crash, j.idempotent
		FROM cron_runs r JOIN cron_jobs j ON j.id = r.job_id WHERE r.rowid = ?1`,
		[]any{row}, &job, &name, &at, &retry, &idem); err != nil {
		return crashClose{}, fmt.Errorf("crashed run %d: %w", row, err)
	}
	if retried, err := requeueCrashed(tx, job, at, retry, idem); err != nil {
		return crashClose{}, err
	} else if retried {
		return crashClose{handled: true, notice: cronNotice{
			key:  fmt.Sprintf("cron-crash:%s:%d", job, at),
			body: fmt.Sprintf("cron %q: slot %s crashed (proses mati); retry_on_crash/idempotent aktif, slot diantre ulang", name, time.UnixMilli(at).UTC().Format(time.RFC3339)),
		}}, nil
	}
	n, err := tx.Exec(`UPDATE cron_runs SET outcome = 'crashed', finished_at = ?3,
			stop_reason = coalesce(stop_reason, '') || ' crash recovered',
			error = 'process died before finish'
		WHERE job_id = ?1 AND scheduled_at = ?2 AND outcome IS NULL`, job, at, now)
	if err != nil {
		return crashClose{}, fmt.Errorf("close crashed run: %w", err)
	}
	if n != 1 {
		return crashClose{}, nil
	}
	return crashClose{handled: true, notice: cronNotice{
		key:  fmt.Sprintf("cron-crash:%s:%d", job, at),
		body: fmt.Sprintf("cron %q: slot %s crashed (proses mati); slot tidak diulang, jadwal berikutnya tetap jalan", name, time.UnixMilli(at).UTC().Format(time.RFC3339)),
	}}, nil
}

// requeueCrashed implements the REL-09 opt-in: only retry_on_crash or
// idempotent=true puts the crashed slot back in the queue.
func requeueCrashed(tx *ctxdb.Tx, job string, at, retry, idem int64) (bool, error) {
	if retry == 0 && idem == 0 {
		return false, nil
	}
	if _, err := tx.Exec(`UPDATE cron_runs SET started_at = NULL, finished_at = NULL,
			stop_reason = coalesce(stop_reason, '') || ' retry_on_crash',
			error = 'process died; retry requested'
		WHERE job_id = ?1 AND scheduled_at = ?2 AND outcome IS NULL`, job, at); err != nil {
		return false, fmt.Errorf("requeue crashed run: %w", err)
	}
	return true, nil
}

// pauseCrashedJobs pauses jobs at pause_after_crashes consecutive crashed
// slots. Only a job that is enabled and not already paused is changed, so a
// repeated recovery never re-notifies.
func pauseCrashedJobs(tx *ctxdb.Tx, now int64) ([]cronNotice, error) {
	ids, err := tx.Strings(`SELECT id FROM cron_jobs WHERE enabled = 1 AND paused_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("pause candidates: %w", err)
	}
	var out []cronNotice
	for _, id := range ids {
		var name string
		var limit int64
		if err := tx.Get(`SELECT name, pause_after_crashes FROM cron_jobs WHERE id = ?1`, []any{id}, &name, &limit); err != nil {
			return nil, fmt.Errorf("pause job %s: %w", id, err)
		}
		var streak int64
		if err := tx.Get(`SELECT count(*) FROM (
				SELECT outcome FROM cron_runs WHERE job_id = ?1
				ORDER BY scheduled_at DESC LIMIT ?2
			) WHERE outcome = 'crashed'`, []any{id, limit}, &streak); err != nil {
			return nil, fmt.Errorf("crash streak %s: %w", id, err)
		}
		if streak < limit {
			continue
		}
		n, err := tx.Exec(`UPDATE cron_jobs SET enabled = 0, paused_at = ?2,
				pause_reason = ?3, updated_at = ?2
			WHERE id = ?1 AND enabled = 1 AND paused_at IS NULL`,
			id, now, fmt.Sprintf("crashed %d× berturut-turut", streak))
		if err != nil {
			return nil, fmt.Errorf("pause job %s: %w", id, err)
		}
		if n != 1 {
			continue
		}
		out = append(out, cronNotice{
			key:  fmt.Sprintf("cron-pause:%s:%d", id, now),
			body: fmt.Sprintf("cron %q di-pause setelah %d crash berturut-turut; jalankan `damnbot cron resume %s`", name, streak, id),
		})
	}
	return out, nil
}
