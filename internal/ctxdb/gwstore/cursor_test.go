package gwstore

import (
	"errors"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func openAt(t *testing.T, dir string) *ctxdb.Store {
	t.Helper()
	db := openDir(t, dir, 0)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close ctxdb: %v", err)
		}
	})
	return db
}

// TestGWCursorPersist proves the GAP-GW-08 cursor row: it is absent until the
// adapter saves one, survives a store restart, and is keyed per account.
func TestGWCursorPersist(t *testing.T) {
	dir := t.TempDir()
	at := time.Unix(1_700_000_000, 0)

	db := openAt(t, dir)
	store := Store{DB: db}
	checkEmpty(t, store)
	if err := store.SetCursor(t.Context(), "email", "bot@example.com", "7:42", at); err != nil {
		t.Fatal(err)
	}
	checkCursor(t, store, "email", "bot@example.com", "7:42")
	if _, found, err := store.Cursor(t.Context(), "email", "other@example.com"); err != nil || found {
		t.Fatalf("second account inherited a cursor: found %v err %v", found, err)
	}
	if _, _, err := store.Cursor(t.Context(), "", "bot@example.com"); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("empty platform = %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	store = Store{DB: openAt(t, dir)}
	checkCursor(t, store, "email", "bot@example.com", "7:42")
	if err := store.SetCursor(t.Context(), "email", "bot@example.com", "7:43", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	checkCursor(t, store, "email", "bot@example.com", "7:43")
}

func checkEmpty(t *testing.T, store Store) {
	t.Helper()
	if _, found, err := store.Cursor(t.Context(), "email", "bot@example.com"); err != nil || found {
		t.Fatalf("empty cursor = found %v err %v", found, err)
	}
}

func checkCursor(t *testing.T, store Store, platform, account, want string) {
	t.Helper()
	got, found, err := store.Cursor(t.Context(), platform, account)
	if err != nil || !found || got != want {
		t.Fatalf("cursor %s/%s = %q found %v err %v, want %q", platform, account, got, found, err, want)
	}
}
