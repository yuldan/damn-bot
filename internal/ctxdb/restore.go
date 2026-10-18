package ctxdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
)

// Restore installs a backup DB under the maintenance barrier (SPEC §11.6
// Backup/restore steps 0–6, §11.11.7). Other processes drain before the
// swap and reopen afterwards (os.SameFile, I8).
func Restore(ctx context.Context, o Options, backup string) (err error) {
	s, err := newStore(o)
	if err != nil {
		return err
	}
	b, err := AcquireBarrier(ctx, o.Dir, s.id, reasonRestore, 30, s.opt.BarrierWait) // (0)
	if err != nil {
		return err
	}
	defer b.Release() // (6) every path removes the marker
	s.barrierOwner.Store(true)
	if err := installDB(backup, DBPath(o.Dir)); err != nil {
		return err
	}
	if s.w, s.r, err = s.openHandles(); err != nil {
		return err
	}
	defer s.detach()
	if s.fi, err = os.Stat(DBPath(o.Dir)); err != nil {
		return fmt.Errorf("ctxdb: restore: %w", err)
	}
	steps := []func() error{
		func() error { return s.tx(ctx, modeRestore, func(t *Tx) error { _, err := t.Exec("DELETE FROM session_leases"); return err }) }, // (1)
		func() error { return s.tx(ctx, modeRestore, (*Tx).restoreMissing) },                                                             // (2)
		func() error { return s.reapplyPurge(ctx) },                                                                                        // (3)
		func() error { return s.tx(ctx, modeNormal, func(t *Tx) error { return t.recoverTrash("", "") }) },                                 // (4)
		func() error { return s.tx(ctx, modeRestore, (*Tx).closeRestoredJournals) },                                                      // (5)
	}
	for _, f := range steps {
		if err := f(); err != nil {
			return err
		}
	}
	return nil
}

// installDB replaces the DB file (and its WAL/SHM) by a copy of src.
func installDB(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("ctxdb: restore: %w", err)
	}
	defer in.Close()
	tmp := dst + ".restore"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("ctxdb: restore: %w", err)
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if err = errors.Join(err, out.Close()); err != nil {
		return fmt.Errorf("ctxdb: restore: %w", err)
	}
	for _, suf := range []string{"-wal", "-shm"} {
		_ = os.Remove(dst + suf)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("ctxdb: restore: %w", err)
	}
	return nil
}

// restoreMissing: every live blobs row without its file → expired{restore_missing}.
func (t *Tx) restoreMissing() error {
	shas, err := t.Strings(`SELECT sha256 FROM blobs b WHERE coalesce((SELECT kind FROM blob_expirations e WHERE e.sha256 = b.sha256 ORDER BY e.id DESC LIMIT 1), '') <> 'expired'`)
	if err != nil {
		return err
	}
	for _, sha := range shas {
		if _, err := os.Stat(t.s.blobPath(sha)); errors.Is(err, os.ErrNotExist) {
			if _, err := t.Exec("INSERT INTO blob_expirations(sha256, kind, reason, at) VALUES (?, 'expired', 'restore_missing', ?)", sha, t.s.clk.WallMs()); err != nil {
				return err
			}
		}
	}
	return nil
}

// reapplyPurge = step (3): one purge, phase 0 only, mode 'purge', with a new
// purge lease owned by the barrier owner. P = ∪(intent.P − excluded) of the
// ledger ∪ sessions labelled in the snapshot (relabelled to the new txid).
// The lease is then released (expires_at = 0) and phases 1–3 are resumed
// normally after the barrier.
func (s *Store) reapplyPurge(ctx context.Context) error {
	var p, out []string
	for _, e := range readLedger(s.dir) {
		switch e.Type {
		case "intent":
			p = append(p, e.P...)
		case "excluded":
			out = append(out, e.IDs...)
		}
	}
	labelled, err := s.strings(ctx, "SELECT DISTINCT session_id FROM session_labels WHERE key = 'purge_state'")
	if err != nil {
		return err
	}
	p = slices.DeleteFunc(append(p, labelled...), func(id string) bool { return slices.Contains(out, id) })
	slices.Sort(p)
	p = slices.Compact(p)
	err = s.tx(ctx, modePurge, func(t *Tx) error {
		live, err := t.Strings("SELECT id FROM sessions WHERE id IN (SELECT value FROM json_each(?1))", jsonOf(p))
		if err == nil {
			p, err = t.descFirst(live)
		}
		if err == nil {
			_, err = t.Exec("DELETE FROM module_state WHERE module_id = 'purge'")
		}
		return err
	})
	if err != nil || len(p) == 0 {
		return err
	}
	txid := newID()
	if err := s.ledger(map[string]any{"type": "intent", "txid": txid, "P": p}); err != nil {
		return fmt.Errorf("ctxdb: purge ledger: %w: %w", ErrDiskFull, err)
	}
	for i := 0; ; i++ {
		var v purgeValue
		err := s.tx(ctx, modePurge, func(t *Tx) (err error) { v, err = t.purgeBatch(txid, p, i == 0); return err })
		if err != nil || v.Phase != 0 {
			return errors.Join(err, s.releasePurge(txid))
		}
	}
}

// closeRestoredJournals = step (5): open journals of unlabelled sessions are
// closed crashed without auto-continue; labelled sessions are skipped (guard
// P stays active in mode 'restore').
func (t *Tx) closeRestoredJournals() error {
	rows, err := t.c.QueryContext(t.ctx, `SELECT turn_id, session_id, coalesce(last_tool_name, '') FROM turn_journal
		WHERE state IN ('running','tool_pending','tool_done','awaiting_confirmation','awaiting_approval') AND `+notPurging("session_id"))
	if err != nil {
		return classify(err)
	}
	var js [][3]string
	for rows.Next() {
		var j [3]string
		if err := rows.Scan(&j[0], &j[1], &j[2]); err != nil {
			_ = rows.Close()
			return err
		}
		js = append(js, j)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, j := range js {
		if err := t.closeCrashed(j[1], j[0], j[2], "restore"); err != nil {
			return err
		}
	}
	return nil
}
