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
	ctx      context.Context //nolint:containedctx // a Tx lives exactly as long as the transaction begun with this ctx
	c        *sql.Conn
	s        *Store
	txid     string
	onCommit []func() error
	trashed  bool // files moved to .trash/<txid>/ (recovered on rollback)
}

type querier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

// each runs a query and calls scan for every row; rows are always closed
// and every error is wrapped and classified.
func each(ctx context.Context, db querier, q string, args []any, scan func(*sql.Rows) error) (err error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return wrap(err)
	}
	defer func() { err = errors.Join(err, wrap(rows.Close())) }()
	for rows.Next() {
		if serr := scan(rows); serr != nil {
			return wrap(serr)
		}
	}
	return wrap(rows.Err())
}

// getOne scans the first row into dest; no row is ErrNotFound.
func getOne(ctx context.Context, db querier, q string, args []any, dest ...any) error {
	found := false
	err := each(ctx, db, q, args, func(r *sql.Rows) error {
		if found {
			return nil
		}
		found = true
		return r.Scan(dest...)
	})
	if err == nil && !found {
		err = fmt.Errorf("ctxdb: %w: %w", ErrNotFound, sql.ErrNoRows)
	}
	return err
}

// column collects the first column of every row.
func column[T any](ctx context.Context, db querier, q string, args ...any) ([]T, error) {
	var out []T
	err := each(ctx, db, q, args, func(r *sql.Rows) error {
		var v T
		out = append(out, v)
		return r.Scan(&out[len(out)-1])
	})
	return out, err
}

// Exec runs one or more statements and returns rows affected by the last.
func (t *Tx) Exec(q string, args ...any) (int64, error) {
	res, err := t.c.ExecContext(t.ctx, q, args...)
	if err != nil {
		return 0, wrap(err)
	}
	n, err := res.RowsAffected()
	return n, wrap(err)
}

// Get scans one row; a missing row is ErrNotFound.
func (t *Tx) Get(q string, args []any, dest ...any) error {
	return getOne(t.ctx, t.c, q, args, dest...)
}

// Strings returns the first column of every row.
func (t *Tx) Strings(q string, args ...any) ([]string, error) {
	return column[string](t.ctx, t.c, q, args...)
}

// Ints returns the first integer column of every row.
func (t *Tx) Ints(q string, args ...any) ([]int64, error) {
	return column[int64](t.ctx, t.c, q, args...)
}

// run executes a named block of queries.sql.
func (t *Tx) run(name string, args ...any) error {
	_, err := t.Exec(t.s.q[name], args...)
	return err
}

// OnCommit runs f after a successful COMMIT (file unlinks, ledger lines).
// Its errors cannot undo the commit; they are kept and returned by Close.
func (t *Tx) OnCommit(f func() error) { t.onCommit = append(t.onCommit, f) }

// Write runs fn in a normal-mode (”) write transaction.
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
	if perr := s.preWrite(ctx); perr != nil {
		return perr
	}
	c, err := s.w.Conn(ctx)
	if err != nil {
		return wrap(err)
	}
	t := &Tx{ctx: ctx, c: c, s: s, txid: fmt.Sprintf("%d-%s-%d", s.id.PID, s.id.Start, s.txSeq.Add(1))}
	if err = s.begin(ctx, c); err != nil {
		return errors.Join(wrap(err), wrap(c.Close()))
	}
	began, committed := time.Now(), false
	defer func() { err = errors.Join(err, s.endTx(ctx, t, mode, began, committed)) }()
	var uv int
	if err = getOne(ctx, c, "PRAGMA user_version", nil, &uv); err == nil && uv > s.schemaMax() {
		err = fmt.Errorf("ctxdb: db version %d > binary %d: %w", uv, s.schemaMax(), ErrSchemaNewer)
	}
	if err != nil {
		return err
	}
	s.mode.Store(mode)
	if ferr := fn(t); ferr != nil {
		return ferr
	}
	if _, err = c.ExecContext(ctx, "COMMIT"); err != nil {
		return wrap(err)
	}
	committed = true
	for _, f := range t.onCommit {
		s.bgErr = errors.Join(s.bgErr, wrap(f()))
	}
	return nil
}

// Writer fairness (ADR-087). SQLite's busy handler polls with growing sleeps
// (≤ 100 ms) and is not FIFO: under a saturated write lock a process that
// commits and immediately begins again keeps winning, so a newcomer can wait
// past busy_timeout (5 s) and see db_busy. Waiters therefore announce a
// pending BEGIN with a shared flock on writerWaitFile; a writer that ends a
// transaction while someone waits does not begin again for writerYield.
// writerYield exceeds the busy handler's longest sleep, so every waiter polls
// at least once while the lock is free; a waiter thus gets the lock within
// (writers − 1) × (hold + writerYield) instead of an unbounded time.
const (
	writerWaitFile = "run/writer.wait"
	writerYield    = 150 * time.Millisecond
)

// begin starts BEGIN IMMEDIATE after this Store's fairness pause, announced
// as a waiter for its whole duration (a failed announcement only loses
// fairness for this one attempt, never correctness).
func (s *Store) begin(ctx context.Context, c *sql.Conn) error {
	if d := time.Until(s.yieldUntil); d > 0 {
		if err := sleepReal(ctx, d); err != nil {
			return err
		}
	}
	f, aerr := tryLock(s.root, writerWaitFile, false)
	_, err := c.ExecContext(ctx, "BEGIN IMMEDIATE")
	err = wrap(err)
	if aerr == nil {
		err = errors.Join(err, unlockFile(f))
	}
	return err
}

// yieldIfWaiters runs after COMMIT/ROLLBACK: an exclusive probe of
// writerWaitFile fails exactly while another writer announces a BEGIN.
func (s *Store) yieldIfWaiters() error {
	if !fairLocks {
		return nil
	}
	// Contention on the probe is the expected signal (a waiter exists), not a
	// failure: yield the next BEGIN instead of returning the error.
	if f, err := tryLock(s.root, writerWaitFile, true); err == nil {
		return unlockFile(f)
	}
	s.yieldUntil = time.Now().Add(writerYield)
	return nil
}

// endTx rolls back an unfinished transaction (and recovers its own trash,
// gc trigger (c)), reports lock time, resets the mode and releases the conn.
func (s *Store) endTx(ctx context.Context, t *Tx, mode string, began time.Time, committed bool) error {
	bg := context.WithoutCancel(ctx)
	var err error
	if !committed {
		_, rerr := t.c.ExecContext(bg, "ROLLBACK")
		err = wrap(rerr)
		if t.trashed {
			t.ctx = bg
			err = errors.Join(err, t.recoverOwnTrash())
		}
	}
	if s.opt.OnTx != nil {
		s.opt.OnTx(mode, time.Since(began))
	}
	s.mode.Store(modeNormal)
	return errors.Join(err, wrap(t.c.Close()), s.yieldIfWaiters())
}

// recoverOwnTrash runs gc step 1 for this txid in a fresh transaction.
func (t *Tx) recoverOwnTrash() error {
	if _, err := t.c.ExecContext(t.ctx, "BEGIN IMMEDIATE"); err != nil {
		return wrap(err)
	}
	if err := t.recoverTrash(t.txid, ""); err != nil {
		_, rerr := t.c.ExecContext(t.ctx, "ROLLBACK")
		return errors.Join(err, wrap(rerr))
	}
	_, err := t.c.ExecContext(t.ctx, "COMMIT")
	return wrap(err)
}

// preWrite enforces I8 before every write transaction (and read): a
// maintenance marker drains this process; a replaced DB file is reopened.
func (s *Store) preWrite(ctx context.Context) error {
	var err error
	if s.w != nil && !s.barrierOwner.Load() && markerPresent(s.root) {
		err = s.detach()
	}
	if s.w != nil {
		if fi, serr := s.root.Stat(dbFile); serr != nil || !os.SameFile(fi, s.fi) {
			err = errors.Join(err, s.detach())
		}
	}
	if err == nil && s.w == nil {
		err = s.attach(ctx)
	}
	return err
}

// reader returns the read pool, reattaching a drained Store first (barrier
// open order) so reads never touch a replaced DB file.
func (s *Store) reader(ctx context.Context) (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.preWrite(ctx)
	return s.r, err
}

func (s *Store) get(ctx context.Context, q string, args []any, dest ...any) error {
	db, err := s.reader(ctx)
	if err != nil {
		return err
	}
	return getOne(ctx, db, q, args, dest...)
}

func (s *Store) each(ctx context.Context, q string, args []any, scan func(*sql.Rows) error) error {
	db, err := s.reader(ctx)
	if err != nil {
		return err
	}
	return each(ctx, db, q, args, scan)
}

func (s *Store) strings(ctx context.Context, q string, args ...any) ([]string, error) {
	db, err := s.reader(ctx)
	if err != nil {
		return nil, err
	}
	return column[string](ctx, db, q, args...)
}

// Count returns a single integer from a read query (tests, doctor).
func (s *Store) Count(ctx context.Context, q string, args ...any) (int64, error) {
	var n int64
	err := s.get(ctx, q, args, &n)
	return n, err
}
