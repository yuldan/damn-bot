package mcpstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func open(t *testing.T) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	})
	return db
}

func openDir(t *testing.T, dir string, schemaMax int) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dir, Kind: "run", SchemaMax: schemaMax})
	if err != nil {
		t.Fatalf("open (schema max %d): %v", schemaMax, err)
	}
	return db
}

func pin() Pin {
	return Pin{
		Server: "srv", Tool: "echo", SchemaSHA256: strings.Repeat("a", 64),
		FirstSeen: 10, ApprovedAt: 10, ApprovedBy: "first-seen", State: StatePinned,
		Diff: `{"change":"initial"}`,
	}
}

func TestPutGetAndList(t *testing.T) {
	t.Parallel()
	store := Store{DB: open(t)}
	ctx := context.Background()
	if err := store.PutPin(ctx, pin()); err != nil {
		t.Fatalf("PutPin: %v", err)
	}
	got, found, err := store.Pin(ctx, "srv", "echo")
	if err != nil || !found || got.SchemaSHA256 != pin().SchemaSHA256 || got.ApprovedAt != 10 {
		t.Fatalf("Pin = %+v, %v, %v", got, found, err)
	}
	if _, found, xerr := store.Pin(ctx, "srv", "missing"); xerr != nil || found {
		t.Fatalf("missing = found %v err %v", found, err)
	}
	list, err := store.Pins(ctx, "srv")
	if err != nil || len(list) != 1 || list[0].Tool != "echo" {
		t.Fatalf("Pins = %+v, %v", list, err)
	}
	// Idempotent rewrite keeps the audit timestamps.
	if err := store.PutPin(ctx, pin()); err != nil {
		t.Fatalf("repeat PutPin: %v", err)
	}
	if n := count(t, store.DB, `SELECT count(*) FROM mcp_tool_pins`); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func TestPutPinRejectsRawOrMalformed(t *testing.T) {
	t.Parallel()
	store := Store{DB: open(t)}
	bad := pin()
	bad.SchemaSHA256 = "xyz"
	if err := store.PutPin(context.Background(), bad); !errors.Is(err, ErrInvalidPin) {
		t.Fatalf("short digest = %v", err)
	}
	bad = pin()
	bad.State = "approved"
	if err := store.PutPin(context.Background(), bad); !errors.Is(err, ErrInvalidPin) {
		t.Fatalf("legacy state = %v", err)
	}
	bad = pin()
	bad.Diff = "{not json"
	if err := store.PutPin(context.Background(), bad); !errors.Is(err, ErrInvalidPin) {
		t.Fatalf("bad diff = %v", err)
	}
}

func TestApproveConditionalAndIdempotent(t *testing.T) {
	t.Parallel()
	store := Store{DB: open(t)}
	ctx := context.Background()
	changed := pin()
	changed.State = StateChanged
	changed.ApprovedAt = 0
	changed.ApprovedBy = ""
	changed.Diff = `{"change":"modified"}`
	if err := store.PutPin(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.ApprovePin(ctx, "srv", "echo", strings.Repeat("b", 64), "op", 20); err != nil || ok {
		t.Fatalf("stale approve = %v, %v", ok, err)
	}
	if ok, err := store.ApprovePin(ctx, "srv", "echo", changed.SchemaSHA256, "op", 20); err != nil || !ok {
		t.Fatalf("approve = %v, %v", ok, err)
	}
	got, found, gerr := store.Pin(ctx, "srv", "echo")
	if gerr != nil || !found {
		t.Fatalf("Pin = found %v err %v", found, gerr)
	}
	if got.State != StatePinned || got.ApprovedBy != "op" || got.FirstSeen != 10 {
		t.Fatalf("approved = %+v", got)
	}
	if ok, err := store.ApprovePin(ctx, "srv", "echo", changed.SchemaSHA256, "op", 21); err != nil || ok {
		t.Fatalf("double approve = %v, %v", ok, err)
	}
}

// TestMigration0017FromEmptyDB: a fresh store reaches 0017 and the pin shape
// rejects legacy columns.
func TestMigration0017FromEmptyDB(t *testing.T) {
	t.Parallel()
	db := open(t)
	if v := count(t, db, `PRAGMA user_version`); v != 17 {
		t.Fatalf("user_version = %d, want 17", v)
	}
	for _, col := range []string{"server_id", "tool_name", "schema_sha256", "first_seen", "approved_at", "approved_by", "state", "diff_summary"} {
		if count(t, db, `SELECT count(*) FROM pragma_table_info('mcp_tool_pins') WHERE name = ?`, col) != 1 {
			t.Fatalf("column %s missing", col)
		}
	}
	if count(t, db, `SELECT count(*) FROM pragma_table_info('mcp_tool_pins') WHERE name = 'pin_sha'`) != 0 {
		t.Fatal("legacy pin_sha column survived 0017")
	}
}

// TestMigration0017From0016: a 0016 database upgrades once and re-running the
// ladder is a no-op with an identical schema (the 0007 table was unwritten).
func TestMigration0017From0016(t *testing.T) {
	dir := t.TempDir()
	old := openDir(t, dir, 16)
	if v := count(t, old, `PRAGMA user_version`); v != 16 {
		t.Fatalf("old user_version = %d, want 16", v)
	}
	if count(t, old, `SELECT count(*) FROM pragma_table_info('mcp_tool_pins') WHERE name = 'state'`) != 0 {
		t.Fatal("0016 already has the 0017 state column; negative control invalid")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	db := openDir(t, dir, 0)
	if v := count(t, db, `PRAGMA user_version`); v != 17 {
		t.Fatalf("upgraded user_version = %d, want 17", v)
	}
	if count(t, db, `SELECT count(*) FROM mcp_tool_pins`) != 0 {
		t.Fatal("upgrade invented pin rows")
	}
	dump := dumpSchema(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	again := openDir(t, dir, 0)
	defer func() {
		if err := again.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	if count(t, again, `PRAGMA user_version`) != 17 || dumpSchema(t, again) != dump {
		t.Fatal("re-running the ladder changed the schema")
	}
}

func count(t *testing.T, db *ctxdb.Store, q string, args ...any) int {
	t.Helper()
	var n int
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error { return tx.Get(q, args, &n) })
	if err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return n
}

func dumpSchema(t *testing.T, db *ctxdb.Store) string {
	t.Helper()
	var dump string
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT group_concat(sql, ';') FROM (SELECT sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type, name, sql)`, nil, &dump)
	})
	if err != nil {
		t.Fatalf("schema dump: %v", err)
	}
	return dump
}
