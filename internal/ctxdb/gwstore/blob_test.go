package gwstore

import (
	"fmt"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func seedBlob(t *testing.T, db *ctxdb.Store, sha, kind, ownerID, session string) {
	t.Helper()
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		if _, xerr := tx.Exec(`INSERT INTO blobs(sha256, size, mime, created_at) VALUES (?1, 1, 'text/plain', 1)
			ON CONFLICT DO NOTHING`, sha); xerr != nil {
			return fmt.Errorf("insert blob: %w", xerr)
		}
		if _, xerr := tx.Exec(`INSERT INTO blob_refs(sha256, owner_kind, owner_id, ref_key, session_id, created_at)
			VALUES (?1, ?2, ?3, 'blob_sha', ?4, 1)`, sha, kind, ownerID, session); xerr != nil {
			return fmt.Errorf("insert blob ref: %w", xerr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestBlobOwnedScoping is ADR-084's storage side: a blob is readable only by
// the identity whose session (newest gateway_chat label) or inbound update
// references it.
func TestBlobOwnedScoping(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	routeA := Route{Platform: "telegram", ChatID: "c-a"}
	routeB := Route{Platform: "telegram", ChatID: "c-b"}
	routed(t, db, "sess-a", routeA, "telegram:a", 1)
	routed(t, db, "sess-b", routeB, "telegram:b", 2)
	for _, sid := range []string{"sess-a", "sess-b"} {
		if _, err := db.AcquireLease(t.Context(), sid); err != nil {
			t.Fatal(err)
		}
	}

	shaA := strings.Repeat("a", 64)
	shaB := strings.Repeat("b", 64)
	shaIn := strings.Repeat("c", 64)
	seedBlob(t, db, shaA, "event", "1", "sess-a")
	seedBlob(t, db, shaB, "event", "2", "sess-b")

	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		if _, xerr := tx.Exec(`INSERT INTO gateway_inbound(platform, external_id, chat_id, sender_id, payload, received_at)
			VALUES ('telegram', 'u:7', 'c-b', 'b', '{}', 1)`); xerr != nil {
			return fmt.Errorf("insert inbound: %w", xerr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	seedBlob(t, db, shaIn, "gateway_inbound", "telegram:u:7", ctxdb.SystemSession)

	for _, c := range []struct {
		name, sha, platform, identity string
		want                          bool
	}{
		{"A reads own session blob", shaA, "telegram", "a", true},
		{"B reads A's session blob", shaA, "telegram", "b", false},
		{"B reads own session blob", shaB, "telegram", "b", true},
		{"A reads B's session blob", shaB, "telegram", "a", false},
		{"B reads own inbound blob", shaIn, "telegram", "b", true},
		{"A reads B's inbound blob", shaIn, "telegram", "a", false},
		{"unknown sha", strings.Repeat("d", 64), "telegram", "a", false},
	} {
		got, berr := store.BlobOwned(t.Context(), c.sha, c.platform, c.identity)
		if berr != nil {
			t.Fatalf("%s: %v", c.name, berr)
		}
		if got != c.want {
			t.Errorf("%s = %v, want %v", c.name, got, c.want)
		}
	}

	// The newest gateway_chat label wins, exactly like Session: re-labelling
	// sess-a to B moves its blob to B and out of A's reach.
	err = db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		if _, xerr := tx.Exec(`INSERT INTO session_labels(session_id, key, value, created_at)
			VALUES ('sess-a', 'gateway_chat', 'telegram:b', 3)`); xerr != nil {
			return fmt.Errorf("relabel session: %w", xerr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.BlobOwned(t.Context(), shaA, "telegram", "a"); err != nil || ok {
		t.Fatalf("after re-label A still reads: ok=%v err=%v", ok, err)
	}
	if ok, err := store.BlobOwned(t.Context(), shaA, "telegram", "b"); err != nil || !ok {
		t.Fatalf("after re-label B cannot read: ok=%v err=%v", ok, err)
	}
}
