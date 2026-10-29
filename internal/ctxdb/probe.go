package ctxdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	db := sql.OpenDB(connector{d: d, dsn: s.probeDSN()})
	defer func() { err = errors.Join(err, wrap(db.Close())) }()
	c, err := db.Conn(ctx)
	if err != nil {
		return wrap(err)
	}
	defer func() { err = errors.Join(err, wrap(c.Close())) }()
	if _, err = c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return wrap(err)
	}
	// ROLLBACK never inherits cancellation: the connection must not return
	// to the pool with an open transaction when the deadline expired.
	_, err = c.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
	return wrap(err)
}

func (s *Store) probeDSN() string {
	configured := fmt.Sprintf("_pragma=busy_timeout(%d)", s.opt.BusyTimeout.Milliseconds())
	return strings.Replace(s.dsn(false), configured, fmt.Sprintf("_pragma=busy_timeout(%d)", probeBusyTimeout.Milliseconds()), 1)
}
