package ctxdb

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// TestCloseWithActiveReaderDoesNotLock: CloseContext must return without
// SQLITE_LOCKED ("database table is locked") or deadlocking while a read
// tx/rows are still open. database/sql's DB.Close does not wait for in-use
// connections, so the invariant is a clean, prompt return — not blocking.
// The PASSIVE checkpoint keeps the live reader from turning shutdown into a
// lock error; after Close, new queries on the old pool must be refused.
func TestCloseWithActiveReaderDoesNotLock(t *testing.T) {
	s, err := Open(t.Context(), &Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	rdb := s.r
	tx, err := rdb.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tx.QueryContext(t.Context(), `SELECT 1 FROM sqlite_master`)
	if err != nil {
		if rerr := tx.Rollback(); rerr != nil {
			t.Fatal(rerr)
		}
		t.Fatal(err)
	}
	if rerr := rows.Err(); rerr != nil {
		t.Fatal(rerr)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	done := make(chan error, 1)
	go func() { done <- s.CloseContext(context.Background()) }()
	select {
	case cerr := <-done:
		if cerr != nil {
			if strings.Contains(cerr.Error(), "database table is locked") {
				t.Fatalf("close with active reader hit SQLITE_LOCKED: %v", cerr)
			}
			t.Fatalf("close with active reader: %v", cerr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close did not finish with an active reader")
	}
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
