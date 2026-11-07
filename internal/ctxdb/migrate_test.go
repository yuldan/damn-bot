package ctxdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	// closeHangGuard only turns a deadlock into a test failure; the outcome is
	// decided by ordering, never by how fast the runner is.
	closeHangGuard = 30 * time.Second
	// blockedCloseGuard is the same guard for the negative control, where the
	// simulated close waits for a release that never comes: any timeout detects
	// it, this value only sets how long the control takes.
	blockedCloseGuard = 250 * time.Millisecond
)

// closeWithReaderHeld calls closeFn while the reader stays open and returns as
// soon as close reports. The caller closes readerReleased only after this
// returns, so a Close that waits for the reader can never finish. hang is a
// hang guard, not a performance budget.
func closeWithReaderHeld(closeFn func() error, readerReleased <-chan struct{}, hang time.Duration) (time.Duration, error) {
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- closeFn() }()
	select {
	case err := <-done:
		select {
		case <-readerReleased:
			return time.Since(start), errors.New("close returned only after the reader was released")
		default:
		}
		if err != nil {
			return time.Since(start), fmt.Errorf("close with active reader: %w", err)
		}
		return time.Since(start), nil
	case <-time.After(hang):
		return time.Since(start), fmt.Errorf("close did not return within %v while the reader was open", hang)
	}
}

// blockedCloseDetected is the negative control: a Close that waits for the
// reader must fail the probe, otherwise an always-passing probe would prove
// nothing about the real one.
func blockedCloseDetected(t *testing.T) {
	t.Helper()
	release := make(chan struct{})
	_, err := closeWithReaderHeld(func() error {
		<-release // stand-in for a Close that waits for the reader to finish
		return nil
	}, release, blockedCloseGuard)
	close(release) // let the simulated close goroutine finish
	if err == nil {
		t.Fatal("a close that waits for the reader was not detected")
	}
}

// openReaderWithHeldRow starts a read transaction whose cursor has consumed the
// first of two rows. The caller keeps tx/rows open across Close and must close
// both afterwards.
func openReaderWithHeldRow(t *testing.T, s *Store) (*sql.Tx, *sql.Rows) {
	t.Helper()
	tx, err := s.r.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tx.QueryContext(t.Context(), `SELECT 1 UNION ALL SELECT 2`)
	if err != nil {
		if rerr := tx.Rollback(); rerr != nil {
			t.Fatal(rerr)
		}
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatalf("read before close: %v", rows.Err())
	}
	var held int
	if err := rows.Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 1 {
		t.Fatalf("read before close = %d, want 1", held)
	}
	return tx, rows
}

// TestCloseWithActiveReaderDoesNotLock: CloseContext must return without
// SQLITE_LOCKED ("database table is locked") and without waiting for an open
// read tx/rows. database/sql's DB.Close does not wait for in-use connections,
// so the invariant is a clean, prompt return while the reader keeps its rows.
// The PASSIVE checkpoint keeps the live reader from turning shutdown into a
// lock error; after Close, new queries on the old pool must be refused.
func TestCloseWithActiveReaderDoesNotLock(t *testing.T) {
	t.Run("blocked close is detected", blockedCloseDetected)

	s, err := Open(t.Context(), &Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	rdb := s.r
	tx, rows := openReaderWithHeldRow(t, s)
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	// The reader is released only after close returns, so close has to finish
	// while this cursor and its transaction are still open.
	readerReleased := make(chan struct{})
	elapsed, cerr := closeWithReaderHeld(func() error {
		return s.CloseContext(context.Background())
	}, readerReleased, closeHangGuard)
	if cerr != nil {
		close(readerReleased)
		if strings.Contains(cerr.Error(), "database table is locked") {
			t.Fatalf("close with active reader hit SQLITE_LOCKED: %v", cerr)
		}
		t.Fatalf("%v", cerr)
	}
	t.Logf("CloseContext returned in %v with the read cursor still open", elapsed)
	// The cursor must still serve rows it had not consumed: close neither
	// waited for nor invalidated the reader.
	if !rows.Next() {
		t.Fatalf("cursor stopped serving after close: %v", rows.Err())
	}
	var next int
	if err := rows.Scan(&next); err != nil {
		t.Fatalf("scan after close: %v", err)
	}
	if next != 2 {
		t.Fatalf("row after close = %d, want 2", next)
	}
	close(readerReleased)
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// The closed pool must refuse new reads promptly instead of hanging.
	qctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var n int
	if err := rdb.QueryRowContext(qctx, `SELECT 1`).Scan(&n); err == nil {
		t.Fatal("query on the closed read pool succeeded")
	}
}
