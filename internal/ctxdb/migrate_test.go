package ctxdb

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// TestCloseWithActiveReader: the read pool is closed before the TRUNCATE
// checkpoint, so an in-flight reader cannot make shutdown return
// SQLITE_LOCKED ("database table is locked").
func TestCloseWithActiveReader(t *testing.T) {
	s, err := Open(t.Context(), &Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.r.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
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
	<-time.After(20 * time.Millisecond)
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case cerr := <-done:
		if cerr != nil {
			t.Fatalf("close with reader: %v", cerr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not finish")
	}
}
