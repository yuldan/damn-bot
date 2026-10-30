package ctxdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// probeBusyTimeout bounds the readiness probe's wait for the SQLite writer
// lock; the store's 5 s handler would outlive the ≤ 1 s /readyz contract.
const probeBusyTimeout = 250 * time.Millisecond

// ProbeWrite proves the data dir accepts a write transaction within the
// caller's deadline (GAP-REL-17 /readyz): BEGIN IMMEDIATE on a private
// connection with probeBusyTimeout, then ROLLBACK (no row or WAL frame
// changes). A busy process writer mutex means this process is writing, so it
// is not an unready signal; the fast path also avoids blocking on it.
func (s *Store) ProbeWrite(ctx context.Context) (err error) {
	if s.opt.ReadOnly {
		return fmt.Errorf("ctxdb: inspection store cannot be written: %w", ErrReadOnly)
	}
	if !s.mu.TryLock() {
		return nil
	}
	defer s.mu.Unlock()
	if perr := s.preWrite(ctx); perr != nil {
		return perr
	}
	d, err := s.driver(true)
	if err != nil {
		return err
	}
	// The budget must be a deadline as well as SQLite's busy_timeout: the
	// handler measures sleeping time and let BEGIN IMMEDIATE overshoot it on a
	// loaded runner, while opening the full store DSN under a held writer lock
	// added up to 800 ms on Windows.
	probeCtx, cancel := context.WithTimeout(ctx, probeBusyTimeout)
	defer cancel()
	db := sql.OpenDB(connector{d: d, dsn: s.probeDSN()})
	defer func() { err = errors.Join(err, wrap(db.Close())) }()
	c, err := db.Conn(probeCtx)
	if err != nil {
		return wrap(probeDeadline(ctx, probeCtx, err))
	}
	defer func() { err = errors.Join(err, wrap(c.Close())) }()
	if _, err = c.ExecContext(probeCtx, "BEGIN IMMEDIATE"); err != nil {
		return wrap(probeDeadline(ctx, probeCtx, err))
	}
	// ROLLBACK never inherits cancellation: the connection must not return
	// to the pool with an open transaction when the deadline expired.
	_, err = c.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
	return wrap(err)
}

// probeDeadline maps the probe's own budget expiring to ErrDBBusy: the store
// is write-locked, not broken. A caller cancellation stays a context error.
func probeDeadline(parent, probe context.Context, err error) error {
	if parent.Err() == nil && probe.Err() != nil {
		return fmt.Errorf("%w: probe budget %s", ErrDBBusy, probeBusyTimeout)
	}
	return err
}

// probeDSN is deliberately minimal: the store DSN applies journal_mode,
// synchronous and checkpoint pragmas that take locks, so under a held writer
// lock they burned hundreds of milliseconds before BEGIN ran. The probe only
// needs a read-write connection and the 250 ms busy budget.
func (s *Store) probeDSN() string {
	return "file:" + dsnPath(s.dir) + "?" +
		fmt.Sprintf("mode=rw&_pragma=busy_timeout(%d)", probeBusyTimeout.Milliseconds())
}
