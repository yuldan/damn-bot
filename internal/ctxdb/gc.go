package ctxdb

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Batch limits shared by gc and purge (§11.6, I11).
const (
	batchDeadline = 150 * time.Millisecond
	gcBatchShas   = 500
	gcBatchRefs   = 20_000
	dutyFactor    = 4 // lock released ≥ 4× batch duration (duty cycle ≤ 20 %)
	tmpMaxAge     = 24 * time.Hour
	gcRetentionMs = 3_600_000 // T_gc = T_cut − 3 600 s
)

// liveRefSQL is true when blob_refs row r has a live owner (§11.6 table);
// ?1 = T_gc (wall ms). PK lookups only, no json_tree.
const liveRefSQL = `(r.owner_kind IN ('event','event_attachment')
 OR (r.owner_kind IN ('file_snapshot','action') AND r.created_at >= ?1)
 OR (r.owner_kind = 'turn_journal' AND EXISTS (SELECT 1 FROM turn_journal j WHERE j.turn_id = r.owner_id
     AND j.state IN ('running','tool_pending','tool_done','awaiting_confirmation','awaiting_approval')))
 OR (r.owner_kind = 'gateway_delivery' AND EXISTS (SELECT 1 FROM gateway_deliveries d WHERE d.id = CAST(r.owner_id AS INTEGER)
     AND d.status IN ('queued','sending','failed','uncertain')))
 OR (r.owner_kind = 'gateway_inbound' AND EXISTS (SELECT 1 FROM gateway_inbound g
     WHERE g.platform = substr(r.owner_id, 1, instr(r.owner_id, ':') - 1)
       AND g.external_id = substr(r.owner_id, instr(r.owner_id, ':') + 1) AND g.event_id IS NULL)))`

const candidateSQL = `SELECT b.sha256, (SELECT count(*) FROM blob_refs r WHERE r.sha256 = b.sha256),
  EXISTS (SELECT 1 FROM blob_refs r WHERE r.sha256 = b.sha256 AND r.owner_kind IN ('file_snapshot','action'))
FROM blobs b
WHERE coalesce((SELECT kind FROM blob_expirations e WHERE e.sha256 = b.sha256 ORDER BY e.id DESC LIMIT 1), '') <> 'expired'
  AND NOT EXISTS (SELECT 1 FROM blob_refs r WHERE r.sha256 = b.sha256 AND ` + liveRefSQL + `)`

// GCStats reports one gc run.
type GCStats struct {
	Expired, OrphanFiles, TmpRemoved, Batches int
	MaxLock                                   time.Duration
}

type gcCand struct {
	sha    string
	refs   int
	retain bool // last refs were action/snapshot → journal_retention
	noRow  bool
}

// GC runs `doctor gc` steps 1–3. scheduled=true requires idle (§11.11.8)
// before every batch and stops when the system becomes busy.
func (s *Store) GC(ctx context.Context, scheduled bool) (GCStats, error) {
	var st GCStats
	if err := s.Write(ctx, func(t *Tx) error { return t.recoverTrash("", "") }); err != nil {
		return st, err
	}
	cands, err := s.gcCandidates(ctx, &st)
	if err != nil {
		return st, err
	}
	for len(cands) > 0 {
		if scheduled {
			if idle, err := s.Idle(ctx); err != nil || !idle {
				return st, err
			}
		}
		n, refs := 0, 0
		for n < len(cands) && n < gcBatchShas && refs+cands[n].refs <= gcBatchRefs {
			refs += cands[n].refs
			n++
		}
		n = max(n, 1)
		start := time.Now()
		done := 0
		if err := s.Write(ctx, func(t *Tx) error { done, err = t.gcBatch(cands[:n], &st); return err }); err != nil {
			return st, err
		}
		d := time.Since(start)
		st.Batches++
		st.MaxLock = max(st.MaxLock, d)
		cands = cands[max(done, 1):]
		if err := s.clk.Sleep(ctx, dutyFactor*d); err != nil {
			return st, fmt.Errorf("ctxdb: gc: %w", err)
		}
	}
	return st, nil
}

// gcCandidates is step 2, outside the lock on the read pool.
func (s *Store) gcCandidates(ctx context.Context, st *GCStats) ([]gcCand, error) {
	tgc := s.clk.WallMs() - tCutMs - gcRetentionMs
	rows, err := s.query(ctx, candidateSQL, tgc)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []gcCand
	for rows.Next() {
		var c gcCand
		if err := rows.Scan(&c.sha, &c.refs, &c.retain); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	orphans, err := s.orphanFiles(ctx, st)
	return append(out, orphans...), err
}

// orphanFiles finds final files without a blobs row and deletes temps > 24 h.
func (s *Store) orphanFiles(ctx context.Context, st *GCStats) ([]gcCand, error) {
	var out []gcCand
	root := filepath.Join(s.dir, "blobs")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && d.Name() == ".trash" {
				return filepath.SkipDir
			}
			return err
		}
		if filepath.Base(filepath.Dir(p)) == "tmp" {
			if fi, err := d.Info(); err == nil && s.clk.WallMs()-fi.ModTime().UnixMilli() > tmpMaxAge.Milliseconds() {
				_ = os.Remove(p)
				st.TmpRemoved++
			}
			return nil
		}
		var n int
		if err := s.get(ctx, "SELECT count(*) FROM blobs WHERE sha256 = ?", []any{d.Name()}, &n); err != nil || n > 0 {
			return err
		}
		out = append(out, gcCand{sha: d.Name(), noRow: true})
		return nil
	})
	return out, err
}

// gcBatch is step 3 for one batch under the lock: re-check each sha, move
// its file to .trash/<txid>/ and record `expired`; stop taking new shas
// after the 150 ms deadline. Returns how many candidates were consumed.
func (t *Tx) gcBatch(cands []gcCand, st *GCStats) (int, error) {
	start := time.Now()
	tgc := t.s.clk.WallMs() - tCutMs - gcRetentionMs
	for i, c := range cands {
		if time.Since(start) >= batchDeadline {
			return i, nil
		}
		var still int
		q := `SELECT count(*) FROM blobs b WHERE b.sha256 = ?2 AND coalesce((SELECT kind FROM blob_expirations e WHERE e.sha256 = b.sha256 ORDER BY e.id DESC LIMIT 1), '') <> 'expired'
		 AND NOT EXISTS (SELECT 1 FROM blob_refs r WHERE r.sha256 = b.sha256 AND ` + liveRefSQL + `)`
		if c.noRow {
			q = "SELECT count(*) = 0 FROM blobs WHERE sha256 = ?2 AND ?1 = ?1"
		}
		if err := t.Get(q, []any{tgc, c.sha}, &still); err != nil {
			return i, err
		}
		if still == 0 {
			continue
		}
		if err := os.Rename(t.s.blobPath(c.sha), filepath.Join(mkdirTrash(t), c.sha)); err != nil {
			if errors.Is(err, fs.ErrNotExist) && !c.noRow {
				err = nil // file already gone: status still becomes expired
			} else {
				continue // e.g. sharing violation: skip without `expired`
			}
		}
		t.OnCommit(func() { _ = os.RemoveAll(t.s.trashDir(t.txid)) })
		if c.noRow {
			st.OrphanFiles++
			continue
		}
		reason := "orphan"
		if c.retain {
			reason = "journal_retention"
		}
		if _, err := t.Exec("INSERT INTO blob_expirations(sha256, kind, reason, at) VALUES (?, 'expired', ?, ?)", c.sha, reason, t.s.clk.WallMs()); err != nil {
			return i, err
		}
		st.Expired++
	}
	return len(cands), nil
}

func mkdirTrash(t *Tx) string {
	d := t.s.trashDir(t.txid)
	_ = os.MkdirAll(d, 0o700)
	t.trashed = true
	return d
}

// tCutMs = T_cut offset: undo horizon (§18.1); actions older than T_cut are
// expired for undo, refs older than T_gc = T_cut − 1 h are released.
const tCutMs = 7 * 24 * 3_600_000

// Idle = 0 active journals with an alive lease and 0 inbound in the last
// 60 s (single definition, §11.11.8). Orphan journals do not count.
func (s *Store) Idle(ctx context.Context) (bool, error) {
	rows, err := s.query(ctx, `SELECT l.owner_pid, l.owner_start, l.owner_host, l.owner_boot_id, l.heartbeat_at, l.expires_at
		FROM turn_journal j JOIN session_leases l ON l.session_id = j.session_id
		WHERE j.state IN ('running','tool_pending','tool_done')`)
	if err != nil {
		return false, classify(err)
	}
	defer rows.Close()
	for rows.Next() {
		var x liveRec
		if err := rows.Scan(&x.PID, &x.Start, &x.Host, &x.BootID, &x.HeartbeatAt, &x.ExpiresAt); err != nil {
			return false, err
		}
		if s.alive(x) {
			return false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	var recent int
	err = s.get(ctx, "SELECT count(*) FROM gateway_inbound WHERE received_at > ?", []any{s.clk.WallMs() - 60_000}, &recent)
	return recent == 0, err
}
