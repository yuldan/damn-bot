package gwstore

import (
	"errors"
	"fmt"
	"testing"

	"go.uber.org/goleak"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

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

// identity inserts one allowlisted identity row (the clearance values are the
// store's input, not its policy).
func identity(t *testing.T, db *ctxdb.Store, platform, externalID, role, workspace string) {
	t.Helper()
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO gateway_identities(platform, external_id, display_name, role, status, strength, workspace, updated_at)
			VALUES (?1, ?2, ?2, ?3, 'allowlisted', 'cryptographic', ?4, 1)`,
			platform, externalID, role, nullableText(workspace))
		if xerr != nil {
			return fmt.Errorf("insert identity: %w", xerr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// routed creates a gateway session labelled with principal and routes the
// given conversation to it at created_at.
func routed(t *testing.T, db *ctxdb.Store, id string, route Route, principal string, createdAt int64) {
	t.Helper()
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		if _, xerr := tx.CreateSession(ctxdb.Session{ID: id, Origin: "gateway", Cwd: "/w"}); xerr != nil {
			return fmt.Errorf("create session: %w", xerr)
		}
		if principal != "" {
			if _, xerr := tx.Exec(`INSERT INTO session_labels(session_id, key, value, created_at)
				VALUES (?1, 'gateway_chat', ?2, 1)`, id, principal); xerr != nil {
				return fmt.Errorf("label session: %w", xerr)
			}
		}
		_, xerr := tx.Exec(`INSERT INTO gateway_conversations(platform, chat_id, thread_id, session_id, created_at)
			VALUES (?1, ?2, ?3, ?4, ?5)`, route.Platform, route.ChatID, route.ThreadID, id, createdAt)
		if xerr != nil {
			return fmt.Errorf("route conversation: %w", xerr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, db *ctxdb.Store, query string, args ...any) int64 {
	t.Helper()
	var n int64
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(query, args, &n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLookupIdentity(t *testing.T) {
	db := open(t)
	identity(t, db, "telegram", "42", "owner", "/srv/ws")
	store := Store{DB: db}

	id, found, err := store.Lookup(t.Context(), "telegram", "42")
	if err != nil || !found {
		t.Fatalf("lookup: %+v found=%v err=%v", id, found, err)
	}
	if id.Role != "owner" || id.Status != "allowlisted" || id.Strength != "cryptographic" || id.Workspace != "/srv/ws" {
		t.Fatalf("identity = %+v", id)
	}
	if id.DisplayName != "42" {
		t.Fatalf("display name = %q", id.DisplayName)
	}

	_, found, err = store.Lookup(t.Context(), "telegram", "unknown")
	if err != nil || found {
		t.Fatalf("unknown identity: found=%v err=%v", found, err)
	}
}

// TestRecordDedupe is the negative control of the idempotency key: ten
// deliveries of one update produce one row and one recorded=true, while a
// second update produces a second row.
func TestRecordDedupe(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	in := Inbound{Platform: "telegram", ChatID: "c1", SenderID: "u1", UpdateID: "u:10", Payload: `{"text":"hi"}`}

	recorded := 0
	for range 10 {
		ok, err := store.Record(t.Context(), &in)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			recorded++
		}
	}
	if recorded != 1 {
		t.Fatalf("recorded=%d, want exactly 1", recorded)
	}
	if n := count(t, db, `SELECT count(*) FROM gateway_inbound`); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	// The kept row is the original one, not a later replay with new content.
	in.Payload = `{"text":"changed"}`
	if ok, err := store.Record(t.Context(), &in); err != nil || ok {
		t.Fatalf("replay: ok=%v err=%v", ok, err)
	}
	var payload string
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT payload FROM gateway_inbound WHERE platform = 'telegram' AND external_id = 'u:10'`, nil, &payload)
	})
	if err != nil || payload != `{"text":"hi"}` {
		t.Fatalf("payload = %q err=%v", payload, err)
	}

	in.UpdateID = "u:11"
	if ok, err := store.Record(t.Context(), &in); err != nil || !ok {
		t.Fatalf("new update: ok=%v err=%v", ok, err)
	}
	if n := count(t, db, `SELECT count(*) FROM gateway_inbound`); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
}

func TestRecordRejectsInvalidInbound(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	cases := []Inbound{
		{Platform: "", ChatID: "c", SenderID: "u", UpdateID: "1", Payload: `{}`},
		{Platform: "telegram", ChatID: "", SenderID: "u", UpdateID: "1", Payload: `{}`},
		{Platform: "telegram", ChatID: "c", SenderID: "", UpdateID: "1", Payload: `{}`},
		{Platform: "telegram", ChatID: "c", SenderID: "u", UpdateID: "", Payload: `{}`},
		{Platform: "telegram", ChatID: "c", SenderID: "u", UpdateID: "1", Payload: `not json`},
		{Platform: "telegram", ChatID: "c", SenderID: "u", UpdateID: "1", Payload: ""},
	}
	for i := range cases {
		if _, err := store.Record(t.Context(), &cases[i]); !errors.Is(err, ErrInvalidInbound) {
			t.Errorf("Record(%+v) err=%v, want ErrInvalidInbound", cases[i], err)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM gateway_inbound`); n != 0 {
		t.Fatalf("invalid inbound wrote %d rows", n)
	}
}

// TestSessionScopedToPrincipal proves isolation on one database and one chat
// route: each principal sees only the session labelled for it, even though the
// other session is newer, and an unrelated principal sees neither.
func TestSessionScopedToPrincipal(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	route := Route{Platform: "telegram", ChatID: "shared"}
	routed(t, db, "sA", route, "telegram:A", 10)
	routed(t, db, "sB", route, "telegram:B", 20)

	for principal, want := range map[string]string{"telegram:A": "sA", "telegram:B": "sB"} {
		got, found, err := store.Session(t.Context(), route, principal)
		if err != nil || !found || got != want {
			t.Fatalf("%s: got=%q found=%v err=%v, want %q", principal, got, found, err, want)
		}
	}
	if got, found, err := store.Session(t.Context(), route, "telegram:C"); err != nil || found {
		t.Fatalf("foreign principal read %q: found=%v err=%v", got, found, err)
	}
	if got, found, err := store.Session(t.Context(), Route{Platform: "telegram", ChatID: "other"}, ""); err != nil || found {
		t.Fatalf("unknown route read %q: found=%v err=%v", got, found, err)
	}
}

// TestSessionUnlabelledVisible: a route without a principal label (local/serve
// sessions) stays readable, but an empty principal can never read a labelled
// session.
func TestSessionUnlabelledVisible(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	route := Route{Platform: "telegram", ChatID: "dm"}
	routed(t, db, "local", route, "", 10)

	if got, found, err := store.Session(t.Context(), route, "telegram:A"); err != nil || !found || got != "local" {
		t.Fatalf("unlabelled: got=%q found=%v err=%v", got, found, err)
	}

	other := Route{Platform: "telegram", ChatID: "dm2"}
	routed(t, db, "labelled", other, "telegram:B", 10)
	if got, found, err := store.Session(t.Context(), other, ""); err != nil || found {
		t.Fatalf("empty principal read labelled session %q: found=%v err=%v", got, found, err)
	}
}
