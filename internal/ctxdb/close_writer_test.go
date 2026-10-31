package ctxdb

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestCloseWithActiveWriterStatement: a checkpoint runs on the writer pool,
// so an open transaction there is SQLITE_LOCKED ("database table is locked"),
// not a Close failure. Close must wait for the active statement, then leave
// the WAL to the next open instead of failing shutdown.
func TestCloseWithActiveWriterStatement(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	ctx := context.Background()
	c, err := s.w.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, xerr := c.ExecContext(ctx, "BEGIN IMMEDIATE"); xerr != nil {
		t.Fatal(xerr)
	}
	rows, err := c.QueryContext(ctx, "SELECT 1 FROM sqlite_master")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	if !rows.Next() {
		t.Fatalf("no sqlite_master row: %v", rows.Err())
	}
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case cerr := <-done:
		t.Fatalf("Close returned while the writer statement was active: %v", cerr)
	case <-time.After(20 * time.Millisecond):
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	// Return the connection with the transaction still open: the state a
	// canceled BEGIN leaves behind (the writer must not lose that transaction).
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case cerr := <-done:
		if cerr != nil {
			t.Fatalf("close with an open writer transaction: %v", cerr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not finish after the writer statement was released")
	}
}

// TestCloseReturnsCheckpointError: only SQLITE_BUSY/LOCKED are shutdown
// states; a real checkpoint error (here: closed writer pool) must still be
// returned by Close.
func TestCloseReturnsCheckpointError(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	if err := s.w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err == nil {
		t.Fatal("Close swallowed a non-lock checkpoint error")
	}
}

// TestFailedBeginRollsBack: a BEGIN that fails after SQLite opened the
// transaction must not return the connection to the pool with that
// transaction; the next write on the same writer would fail (and a shutdown
// checkpoint would report SQLITE_LOCKED).
func TestFailedBeginRollsBack(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	ctx := context.Background()
	c, err := s.w.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, xerr := c.ExecContext(ctx, "BEGIN IMMEDIATE"); xerr != nil {
		t.Fatal(xerr)
	}
	if cerr := c.Close(); cerr != nil { // pooled with the transaction open
		t.Fatal(cerr)
	}
	err = s.Write(ctx, func(*Tx) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "within a transaction") {
		t.Fatalf("BEGIN on a pooled open transaction = %v, want the SQLite error", err)
	}
	if err := s.Write(ctx, func(tx *Tx) error {
		_, xerr := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at)
			VALUES ('t', 'k', '', 1)`)
		return xerr
	}); err != nil {
		t.Fatalf("writer connection still poisoned after a failed BEGIN: %v", err)
	}
}

// TestClosedStoreRejectsOperations: after Close the store must not attach
// handles again (that leaked a second pool and skipped close accounting).
func TestClosedStoreRejectsOperations(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(context.Background(), func(*Tx) error { return nil }); !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("write after Close = %v, want ErrConnDone", err)
	}
	if _, err := s.Count(context.Background(), `SELECT 1`); !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("read after Close = %v, want ErrConnDone", err)
	}
}
