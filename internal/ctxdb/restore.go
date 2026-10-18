package ctxdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// Restore installs a backup DB under the maintenance barrier (SPEC §11.6
// Backup/restore steps 0–6, §11.11.7). Other processes drain before the
// swap and reopen afterwards (os.SameFile, I8).
func Restore(ctx context.Context, o *Options, backup string) (err error) {
	s, err := newStore(o)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.closeRoot()) }()
	b, err := AcquireBarrier(ctx, o.Dir, s.id, reasonRestore, 30, s.opt.BarrierWait) // (0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, b.Release()) }() // (6) removes the marker
	s.barrierOwner.Store(true)
	if ierr := s.installDB(backup); ierr != nil {
		return ierr
	}
	if s.w, s.r, err = s.openHandles(ctx); err == nil {
		fi, serr := s.root.Stat(dbFile)
		s.fi, err = fi, wrap(serr)
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.detach()) }()
	steps := []func() error{
		func() error { return s.tx(ctx, modeRestore, deleteLeases) },         // (1)
		func() error { return s.tx(ctx, modeRestore, (*Tx).restoreMissing) }, // (2)
		func() error { return s.reapplyPurge(ctx) },                          // (3)
		func() error { return s.tx(ctx, modeNormal, (*Tx).recoverTrashAll) }, // (4)
		func() error { return s.tx(ctx, modeRestore, (*Tx).closeJournals) },  // (5)
	}
	for _, f := range steps {
		if err := f(); err != nil {
			return err
		}
	}
	return nil
}

func deleteLeases(t *Tx) error {
	_, err := t.Exec("DELETE FROM session_leases")
	return err
}

func (t *Tx) recoverTrashAll() error { return t.recoverTrash("", "") }

func (s *Store) closeRoot() error { return wrap(s.root.Close()) }

// installDB replaces the DB file (and its WAL/SHM) by a copy of backup.
func (s *Store) installDB(backup string) error {
	in, err := os.Open(filepath.Clean(backup)) // backup = caller-provided path (user CLI argument)
	if err != nil {
		return wrap(err)
	}
	tmp := dbFile + ".restore"
	out, err := s.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err == nil {
		_, err = io.Copy(out, in)
	}
	if err == nil {
		err = out.Sync()
	}
	if out != nil {
		err = errors.Join(err, out.Close())
	}
	err = errors.Join(err, in.Close())
	for _, suf := range []string{"-wal", "-shm"} {
		err = errors.Join(err, removeIfExists(s.root, dbFile+suf))
	}
	if err == nil {
		err = s.root.Rename(tmp, dbFile)
	}
	return wrap(err)
}

// restoreMissing: every live blobs row without its file → expired{restore_missing}.
func (t *Tx) restoreMissing() error {
	shas, err := t.Strings(`SELECT sha256 FROM blobs b WHERE coalesce((SELECT kind FROM blob_expirations e WHERE e.sha256 = b.sha256 ORDER BY e.id DESC LIMIT 1), '') <> 'expired'`)
	for i := 0; err == nil && i < len(shas); i++ {
		if _, serr := t.s.root.Stat(blobName(shas[i])); errors.Is(serr, fs.ErrNotExist) {
			_, err = t.Exec("INSERT INTO blob_expirations(sha256, kind, reason, at) VALUES (?, 'expired', 'restore_missing', ?)", shas[i], t.s.clk.WallMs())
		}
	}
	return err
}

// reapplyPurge = step (3): one purge, phase 0 only, mode 'purge', with a new
// purge lease owned by the barrier owner. P = ∪(intent.P − excluded) of the
// ledger ∪ sessions labelled in the snapshot (relabelled to the new txid).
// The lease is then released (expires_at = 0) and phases 1–3 are resumed
// normally after the barrier.
func (s *Store) reapplyPurge(ctx context.Context) error {
	var p, out []string
	for _, e := range readLedger(s.root) {
		switch e.Type {
		case "intent":
			p = append(p, e.P...)
		case "excluded":
			out = append(out, e.IDs...)
		}
	}
	labelled, err := s.strings(ctx, "SELECT DISTINCT session_id FROM session_labels WHERE key = 'purge_state'")
	if err == nil {
		p = slices.DeleteFunc(append(p, labelled...), func(id string) bool { return slices.Contains(out, id) })
		slices.Sort(p)
		p = slices.Compact(p)
		err = s.tx(ctx, modePurge, func(t *Tx) error {
			live, lerr := t.Strings("SELECT id FROM sessions WHERE id IN (SELECT value FROM json_each(?1))", jsonOf(p))
			var first error
			if lerr == nil {
				p, first = t.Strings(t.s.q["desc_first"], jsonOf(live))
				lerr = first
			}
			if lerr == nil {
				_, lerr = t.Exec("DELETE FROM module_state WHERE module_id = 'purge'")
			}
			return lerr
		})
	}
	if err != nil || len(p) == 0 {
		return err
	}
	txid := newID()
	if err = s.ledger(map[string]any{"type": "intent", "txid": txid, "P": p}); err != nil {
		return fmt.Errorf("ctxdb: purge ledger: %w: %w", ErrDiskFull, err)
	}
	for i := 0; ; i++ {
		var v purgeValue
		if err = s.tx(ctx, modePurge, func(t *Tx) (berr error) { v, berr = t.purgeBatch(txid, p, i == 0); return berr }); err != nil || v.Phase != 0 {
			return errors.Join(err, s.releasePurge(ctx, txid))
		}
	}
}

// closeJournals = step (5): open journals of unlabelled sessions are closed
// crashed without auto-continue; labelled sessions are skipped (guard P stays
// active in mode 'restore').
func (t *Tx) closeJournals() error {
	var js [][3]string
	err := each(t.ctx, t.c, `SELECT turn_id, session_id, coalesce(last_tool_name, '') FROM turn_journal
		WHERE state IN ('running','tool_pending','tool_done','awaiting_confirmation','awaiting_approval') AND `+notPurging("session_id"),
		nil, func(r *sql.Rows) error {
			var j [3]string
			if err := r.Scan(&j[0], &j[1], &j[2]); err != nil {
				return wrap(err)
			}
			js = append(js, j)
			return nil
		})
	for i := 0; err == nil && i < len(js); i++ {
		err = t.closeCrashed(js[i][1], js[i][0], js[i][2], "restore")
	}
	return err
}
