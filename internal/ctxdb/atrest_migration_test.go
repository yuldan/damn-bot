package ctxdb

import (
	"context"
	"database/sql"
	"testing"
)

// SEC7-34: migration 0009 must not UPDATE memory (it is append-only). A DB
// with memory rows created before 0009 must still open.
func TestMigration0009NoMemoryUpdate(t *testing.T) {
	db, err := sql.Open("sqlite", t.TempDir()+"/old.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	migs, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migs[:8] {
		if _, err := db.ExecContext(context.Background(), m.body); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
	}
	if _, err := db.ExecContext(context.Background(), `DROP TRIGGER memory_ai; DROP TRIGGER guard_memory_ins`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO memory(kind,category,scope,content,origin,created_at) VALUES ('memory','fact','user','x','user',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), migs[8].body); err != nil {
		t.Fatalf("0009 on memory row: %v", err)
	}
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM memory WHERE content_len IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("content_len null rows = %d, %v", n, err)
	}
}
