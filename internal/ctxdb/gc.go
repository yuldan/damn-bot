package ctxdb

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path"
	"time"
)

// Batch limits shared by gc and purge (§11.6, I11).
const (
	batchDeadline = 150 * time.Millisecond
	gcBatchShas   = 500
	gcBatchRefs   = 20_000
	dutyFactor    = 4 // lock released ≥ 4× batch duration (duty cycle ≤ 20 %)
	tmpMaxAge     = 24 * time.Hour
	// tCutMs is the undo horizon T_cut (§18.1); refs of actions/snapshots are
	// released at T_gc = T_cut − 1 h.
	tCutMs        = 7 * 24 * 3_600_000
	gcRetentionMs = 3_600_000
)

// GCStats reports one gc run.
type GCStats struct {
	Expired, OrphanFiles, TmpRemoved, Batches int
	MaxLock                                   time.Duration
}

type gcCand struct {
	sha    string
	refs   int
	retain bool // last refs were action/snapshot → journal_retention
	noRow  bool // final file without a blobs row (crashed writer)
}

func (s *Store) tGC() int64 { return s.clk.WallMs() - tCutMs - gcRetentionMs }

// GC runs `doctor gc` steps 1–3. scheduled=true requires idle (§11.11.8)
// before every batch and stops when the system becomes busy.
func (s *Store) GC(ctx context.Context, scheduled bool) (GCStats, error) {
	var st GCStats
	err := s.Write(ctx, func(t *Tx) error { return t.recoverTrash("", "") })
	var cands []gcCand
	if err == nil {
		cands, err = s.gcCandidates(ctx, &st)
	}
	for err == nil && len(cands) > 0 {
		if scheduled {
			if idle, ierr := s.Idle(ctx); ierr != nil || !idle {
				return st, ierr
			}
		}
		n, refs := 0, 0
		for n < len(cands) && n < gcBatchShas && refs+cands[n].refs <= gcBatchRefs {
			refs += cands[n].refs
			n++
		}
		start, done := time.Now(), 0
		err = s.Write(ctx, func(t *Tx) (berr error) { done, berr = t.gcBatch(cands[:max(n, 1)], &st); return berr })
		d := time.Since(start)
		st.Batches++
		st.MaxLock = max(st.MaxLock, d)
		cands = cands[max(done, 1):]
		if err == nil {
			err = wrap(s.clk.Sleep(ctx, dutyFactor*d))
		}
	}
	return st, err
}

// gcCandidates is step 2, outside the lock on the read pool.
func (s *Store) gcCandidates(ctx context.Context, st *GCStats) ([]gcCand, error) {
	var out []gcCand
	err := s.each(ctx, s.q["gc.candidates"], []any{s.tGC()}, func(r *sql.Rows) error {
		var c gcCand
		if err := wrap(r.Scan(&c.sha, &c.refs, &c.retain)); err != nil {
			return err
		}
		out = append(out, c)
		return nil
	})
	if err != nil {
		return nil, err
	}
	orphans, err := s.orphanFiles(ctx, st)
	return append(out, orphans...), err
}

// orphanFiles finds final files without a blobs row and deletes temps > 24 h.
func (s *Store) orphanFiles(ctx context.Context, st *GCStats) ([]gcCand, error) {
	var out []gcCand
	err := fs.WalkDir(s.root.FS(), "blobs", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && d.Name() == ".trash":
			return fs.SkipDir
		case d.IsDir():
			return nil
		case path.Dir(p) == "blobs/tmp":
			return s.dropOldTemp(p, d, st)
		}
		var n int
		if gerr := s.get(ctx, "SELECT count(*) FROM blobs WHERE sha256 = ?", []any{d.Name()}, &n); gerr != nil || n > 0 {
			return gerr
		}
		out = append(out, gcCand{sha: d.Name(), noRow: true})
		return nil
	})
	return out, wrap(err)
}

func (s *Store) dropOldTemp(p string, d fs.DirEntry, st *GCStats) error {
	fi, err := d.Info()
	err = wrap(err)
	if err != nil || s.clk.WallMs()-fi.ModTime().UnixMilli() <= tmpMaxAge.Milliseconds() {
		return err
	}
	st.TmpRemoved++
	return removeIfExists(s.root, p)
}

// gcBatch is step 3 for one batch under the lock: re-check each sha, move
// its file to .trash/<txid>/ and record `expired`; stop taking new shas
// after the 150 ms deadline. Returns how many candidates were consumed.
func (t *Tx) gcBatch(cands []gcCand, st *GCStats) (int, error) {
	start := time.Now()
	for i, c := range cands {
		if time.Since(start) >= batchDeadline {
			return i, nil
		}
		q := t.s.q["gc.recheck"]
		if c.noRow {
			q = "SELECT count(*) = 0 FROM blobs WHERE sha256 = ?2 AND ?1 = ?1"
		}
		var still int
		if err := t.Get(q, []any{t.s.tGC(), c.sha}, &still); err != nil {
			return i, err
		}
		if still == 0 {
			continue
		}
		// rename failure (e.g. sharing violation) skips the sha without
		// `expired`; an already missing file still gets its status.
		if terr := t.trashFile(c.sha); terr != nil && (c.noRow || !errors.Is(terr, fs.ErrNotExist)) {
			continue
		}
		if c.noRow {
			st.OrphanFiles++
			continue
		}
		reason := map[bool]string{true: "journal_retention", false: "orphan"}[c.retain]
		if _, err := t.Exec("INSERT INTO blob_expirations(sha256, kind, reason, at) VALUES (?, 'expired', ?, ?)", c.sha, reason, t.s.clk.WallMs()); err != nil {
			return i, err
		}
		st.Expired++
	}
	return len(cands), nil
}

// Idle = 0 active journals with an alive lease and 0 inbound in the last
// 60 s (single definition, §11.11.8). Orphan journals do not count.
func (s *Store) Idle(ctx context.Context) (bool, error) {
	busy := false
	err := s.each(ctx, s.q["gc.idle_leases"], nil, func(r *sql.Rows) error {
		var x liveRec
		if err := wrap(r.Scan(&x.PID, &x.Start, &x.Host, &x.BootID, &x.HeartbeatAt, &x.ExpiresAt)); err != nil {
			return err
		}
		busy = busy || s.alive(x)
		return nil
	})
	var recent int
	if err == nil && !busy {
		err = s.get(ctx, "SELECT count(*) FROM gateway_inbound WHERE received_at > ?", []any{s.clk.WallMs() - 60_000}, &recent)
	}
	return err == nil && !busy && recent == 0, err
}
