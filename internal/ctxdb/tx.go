package ctxdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"
)

// Connection modes of dmb_maint() (SPEC §11.11.3).
const (
	modeNormal  = ""
	modePurge   = "purge"
	modeRestore = "restore"
)

// Tx is one BEGIN IMMEDIATE write transaction on the process writer.
type Tx struct {
	ctx      context.Context
	c        *sql.Conn
	s        *Store
	txid     string
	onCommit []func()
	trashed  bool            // files moved to .trash/<txid>/ (recovered on rollback)
	freed    map[string]bool // purge: shas whose refs were deleted here (step 25)
}

// Exec runs a statement and returns rows affected; errors are classified.
func (t *Tx) Exec(q string, args ...any) (int64, error) {
	res, err := t.c.ExecContext(t.ctx, q, args...)
	if err != nil {
		return 0, classify(err)
	}
	return res.RowsAffected()
}

// Get scans one row; a missing row is ErrNotFound.
func (t *Tx) Get(q string, args []any, dest ...any) error {
	err := t.c.QueryRowContext(t.ctx, q, args...).Scan(dest...)
	if isNoRows(err) {
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	return classify(err)
}

// Strings returns the first column of every row.
func (t *Tx) Strings(q string, args ...any) ([]string, error) {
	rows, err := t.c.QueryContext(t.ctx, q, args...)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// OnCommit runs f after a successful COMMIT (file unlinks, token updates).
func (t *Tx) OnCommit(f func()) { t.onCommit = append(t.onCommit, f) }

// Write runs fn in a normal-mode ('') write transaction.
func (s *Store) Write(ctx context.Context, fn func(*Tx) error) error {
	return s.tx(ctx, modeNormal, fn)
}

// tx is the only place a write transaction starts. The mode is bound to the
// transaction: set after BEGIN IMMEDIATE, reset by the commit/rollback hooks
// and by the deferred cleanup, so an abort (error, Goexit, dead ctx) never
// leaks 'purge'/'restore' into the next transaction (I6).
func (s *Store) tx(ctx context.Context, mode string, fn func(*Tx) error) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.preWrite(ctx); err != nil {
		return err
	}
	c, err := s.w.Conn(ctx)
	if err != nil {
		return classify(err)
	}
	committed := false
	var t *Tx
	var began time.Time
	defer func() {
		bg := context.WithoutCancel(ctx)
		if !committed {
			_, _ = c.ExecContext(bg, "ROLLBACK")
			if t != nil && t.trashed { // trigger (c): owner recovers its own .trash/<txid>/
				err = errors.Join(err, recoverOwnTrash(bg, c, s, t.txid))
			}
		}
		if s.opt.OnTx != nil && !began.IsZero() {
			s.opt.OnTx(mode, time.Since(began))
		}
		s.mode.Store(modeNormal)
		err = joinClose(err, c)
	}()
	if _, err := c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		committed = true // nothing to roll back
		return classify(err)
	}
	began = time.Now()
	var uv int
	if err := c.QueryRowContext(ctx, "PRAGMA user_version").Scan(&uv); err != nil {
		return classify(err)
	}
	if uv > s.schemaMax() {
		return fmt.Errorf("ctxdb: db version %d > binary %d: %w", uv, s.schemaMax(), ErrSchemaNewer)
	}
	s.mode.Store(mode)
	t = &Tx{ctx: ctx, c: c, s: s, txid: fmt.Sprintf("%d-%s-%d", s.id.PID, s.id.Start, s.txSeq.Add(1)), freed: map[string]bool{}}
	if err := fn(t); err != nil {
		return err
	}
	if _, err := c.ExecContext(ctx, "COMMIT"); err != nil {
		return classify(err)
	}
	committed = true
	for _, f := range t.onCommit {
		f()
	}
	return nil
}

// recoverOwnTrash runs gc step 1 for one txid in a fresh transaction.
func recoverOwnTrash(ctx context.Context, c *sql.Conn, s *Store, txid string) error {
	if _, err := c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return classify(err)
	}
	t := &Tx{ctx: ctx, c: c, s: s, txid: txid}
	if err := t.recoverTrash(txid, ""); err != nil {
		_, _ = c.ExecContext(ctx, "ROLLBACK")
		return err
	}
	_, err := c.ExecContext(ctx, "COMMIT")
	return classify(err)
}

func joinClose(err error, c *sql.Conn) error {
	if cerr := c.Close(); err == nil {
		return cerr
	}
	return err
}

// preWrite enforces I8 before every write transaction: a maintenance marker
// drains this process; a replaced DB file is reopened (os.SameFile).
func (s *Store) preWrite(ctx context.Context) error {
	if s.w != nil && markerPresent(s.dir) && !s.barrierOwner.Load() {
		s.detach()
	}
	if s.w != nil {
		if fi, err := os.Stat(DBPath(s.dir)); err != nil || !os.SameFile(fi, s.fi) {
			s.detach()
		}
	}
	if s.w == nil {
		return s.attach(ctx)
	}
	return nil
}

// read helpers on the read-only pool -------------------------------------

// query runs a read query, reattaching a drained Store first (barrier open
// order) so reads never touch a replaced DB file.
func (s *Store) query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	s.mu.Lock()
	err := s.preWrite(ctx)
	db := s.r
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, q, args...)
	return rows, classify(err)
}

func (s *Store) get(ctx context.Context, q string, args []any, dest ...any) error {
	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return classify(err)
		}
		return fmt.Errorf("%w: %w", ErrNotFound, errNoRows)
	}
	return classify(rows.Scan(dest...))
}

// Count returns a single integer from a read query (tests, doctor).
func (s *Store) Count(ctx context.Context, q string, args ...any) (int64, error) {
	var n int64
	err := s.get(ctx, q, args, &n)
	return n, err
}
