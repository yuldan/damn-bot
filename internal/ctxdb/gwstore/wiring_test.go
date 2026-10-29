package gwstore

import (
	"errors"
	"fmt"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// openDir opens a store on an explicit directory and schema ceiling (0 = all
// embedded migrations), so a test can build the 0014 schema first.
func openDir(t *testing.T, dir string, schemaMax int) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dir, Kind: "run", SchemaMax: schemaMax})
	if err != nil {
		t.Fatalf("open (schema max %d): %v", schemaMax, err)
	}
	return db
}

func closeStore(t *testing.T, db *ctxdb.Store) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func query1[T any](t *testing.T, db *ctxdb.Store, q string, args ...any) T {
	t.Helper()
	var v T
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error { return tx.Get(q, args, &v) })
	if err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return v
}

func userVersion(t *testing.T, db *ctxdb.Store) int {
	t.Helper()
	return query1[int](t, db, `PRAGMA user_version`)
}

func tableExists(t *testing.T, db *ctxdb.Store, name string) bool {
	t.Helper()
	return query1[int](t, db, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?1`, name) == 1
}

func columnExists(t *testing.T, db *ctxdb.Store, table, col string) bool {
	t.Helper()
	return query1[int](t, db, `SELECT count(*) FROM pragma_table_info(?1) WHERE name = ?2`, table, col) == 1
}

func schemaDump(t *testing.T, db *ctxdb.Store) string {
	t.Helper()
	return query1[string](t, db, `SELECT group_concat(sql, ';') FROM (SELECT sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type, name, sql)`)
}

// TestMigration0015FromEmptyDB: a fresh store reaches 0015 and the wiring
// columns/tables exist.
func TestMigration0015FromEmptyDB(t *testing.T) {
	db := open(t)
	if v := userVersion(t, db); v != 15 {
		t.Fatalf("user_version = %d, want 15", v)
	}
	for _, tbl := range []string{"gateway_message_ids", "gateway_callbacks"} {
		if !tableExists(t, db, tbl) {
			t.Fatalf("table %s missing on a fresh schema", tbl)
		}
	}
	if !columnExists(t, db, "gateway_inbound", "thread_id") {
		t.Fatal("gateway_inbound.thread_id missing")
	}
	if d := query1[string](t, db, `SELECT dflt_value FROM pragma_table_info('gateway_inbound') WHERE name = 'thread_id'`); d != "''" {
		t.Fatalf("thread_id default = %q, want ''", d)
	}
}

// TestMigration0015From0014: a 0014 database with a queued inbound row upgrades
// to 0015 (the old row gets thread_id ”), and re-running the ladder is a no-op
// with an identical schema.
func TestMigration0015From0014(t *testing.T) {
	dir := t.TempDir()
	old := openDir(t, dir, 14)
	if v := userVersion(t, old); v != 14 {
		t.Fatalf("old user_version = %d, want 14", v)
	}
	if columnExists(t, old, "gateway_inbound", "thread_id") || tableExists(t, old, "gateway_callbacks") {
		t.Fatal("0014 already has the 0015 objects; the negative control is invalid")
	}
	err := old.Write(t.Context(), func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO gateway_inbound(platform, external_id, chat_id, sender_id, payload, received_at)
			VALUES ('telegram', 'u:1', 'c1', 's1', '{"text":"queued"}', 1)`)
		if xerr != nil {
			return fmt.Errorf("insert 0014 row: %w", xerr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("insert 0014 row: %v", err)
	}
	closeStore(t, old)

	db := openDir(t, dir, 0)
	if v := userVersion(t, db); v != 15 {
		t.Fatalf("migrated user_version = %d, want 15", v)
	}
	if !columnExists(t, db, "gateway_inbound", "thread_id") || !tableExists(t, db, "gateway_message_ids") {
		t.Fatal("0015 objects missing after the upgrade")
	}
	var thread string
	err = db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT thread_id FROM gateway_inbound WHERE external_id = 'u:1'`, nil, &thread)
	})
	if err != nil || thread != "" {
		t.Fatalf("pre-0015 row thread_id = %q, err=%v; want \"\"", thread, err)
	}

	dump := schemaDump(t, db)
	closeStore(t, db)

	again := openDir(t, dir, 0)
	defer closeStore(t, again)
	if v := userVersion(t, again); v != 15 || schemaDump(t, again) != dump {
		t.Fatalf("re-run changed the schema: version %d", v)
	}
	if n := count(t, again, `SELECT count(*) FROM gateway_inbound`); n != 1 {
		t.Fatalf("rows after re-run = %d, want 1", n)
	}
}

// TestRecordThreadIDSurvivesReopen: Record stores the topic, the guard keeps it
// immutable, and a reopened store reads the same value back.
func TestRecordThreadIDSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	db := openDir(t, dir, 0)
	store := Store{DB: db}
	in := Inbound{Platform: "telegram", ChatID: "c1", ThreadID: "77", SenderID: "s1", UpdateID: "u:2", Payload: `{"text":"hi"}`}
	if ok, err := store.Record(t.Context(), &in); err != nil || !ok {
		t.Fatalf("record: ok=%v err=%v", ok, err)
	}

	guard := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`UPDATE gateway_inbound SET thread_id = 'other' WHERE external_id = 'u:2'`)
		if xerr != nil {
			return fmt.Errorf("update thread_id: %w", xerr)
		}
		return nil
	})
	if guard == nil {
		t.Fatal("guard accepted an UPDATE of thread_id")
	}
	closeStore(t, db)

	db = openDir(t, dir, 0)
	defer closeStore(t, db)
	var thread, chat string
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT thread_id, chat_id FROM gateway_inbound WHERE external_id = 'u:2'`, nil, &thread, &chat)
	})
	if err != nil || thread != "77" || chat != "c1" {
		t.Fatalf("after reopen: thread=%q chat=%q err=%v", thread, chat, err)
	}
}

// TestClaimCallbackOnce: the first claim wins and every replay is
// ErrAlreadyClaimed with no second row.
func TestClaimCallbackOnce(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	ctx := t.Context()
	if err := store.ClaimCallback(ctx, "cb:1"); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := store.ClaimCallback(ctx, "cb:1"); !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("replay = %v, want ErrAlreadyClaimed", err)
	}
	if err := store.ClaimCallback(ctx, "cb:2"); err != nil {
		t.Fatalf("other callback: %v", err)
	}
	if n := count(t, db, `SELECT count(*) FROM gateway_callbacks`); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
	if err := store.ClaimCallback(ctx, ""); !errors.Is(err, ErrInvalidCallback) {
		t.Fatalf("empty id = %v, want ErrInvalidCallback", err)
	}
}

// TestMessageIDUncertain: 0 records an uncertain send (kirim timeout) and a
// replay of the same value writes nothing.
func TestMessageIDUncertain(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	ctx := t.Context()

	if id, found, err := store.MessageID(ctx, "k1"); err != nil || found || id != 0 {
		t.Fatalf("unknown key: id=%d found=%v err=%v", id, found, err)
	}
	for range 2 {
		if err := store.PutMessageID(ctx, "k1", 0); err != nil {
			t.Fatalf("put uncertain: %v", err)
		}
	}
	if id, found, err := store.MessageID(ctx, "k1"); err != nil || !found || id != 0 {
		t.Fatalf("uncertain read: id=%d found=%v err=%v", id, found, err)
	}
	if n := count(t, db, `SELECT count(*) FROM gateway_message_ids`); n != 1 {
		t.Fatalf("uncertain rows = %d, want 1", n)
	}
}

// TestMessageIDUpgradeFromUncertain: a retry that finally learns the id fills
// the uncertain 0 in once.
func TestMessageIDUpgradeFromUncertain(t *testing.T) {
	store := Store{DB: open(t)}
	ctx := t.Context()
	if err := store.PutMessageID(ctx, "k1", 0); err != nil {
		t.Fatalf("put uncertain: %v", err)
	}
	if err := store.PutMessageID(ctx, "k1", 4242); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if id, found, err := store.MessageID(ctx, "k1"); err != nil || !found || id != 4242 {
		t.Fatalf("upgraded read: id=%d found=%v err=%v", id, found, err)
	}
}

// TestMessageIDUpgrade: a real id replay is a no-op and any later change is a
// conflict (the edit stream never retargets a different message).
func TestMessageIDUpgrade(t *testing.T) {
	store := Store{DB: open(t)}
	ctx := t.Context()

	for range 2 {
		if err := store.PutMessageID(ctx, "k1", 4242); err != nil {
			t.Fatalf("put real id: %v", err)
		}
	}
	if id, found, err := store.MessageID(ctx, "k1"); err != nil || !found || id != 4242 {
		t.Fatalf("real read: id=%d found=%v err=%v", id, found, err)
	}
	if err := store.PutMessageID(ctx, "k1", 9); !errors.Is(err, ErrMessageIDConflict) {
		t.Fatalf("retarget = %v, want ErrMessageIDConflict", err)
	}
	if err := store.PutMessageID(ctx, "k1", 0); !errors.Is(err, ErrMessageIDConflict) {
		t.Fatalf("downgrade = %v, want ErrMessageIDConflict", err)
	}
	if err := store.PutMessageID(ctx, "", 1); !errors.Is(err, ErrInvalidMessageID) {
		t.Fatalf("empty key = %v, want ErrInvalidMessageID", err)
	}
	if err := store.PutMessageID(ctx, "k2", -1); !errors.Is(err, ErrInvalidMessageID) {
		t.Fatalf("negative id = %v, want ErrInvalidMessageID", err)
	}
}
