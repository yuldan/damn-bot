package ctxdb

import (
	"errors"
	"testing"
)

// newerDB creates a data dir whose user_version exceeds this binary's
// schema, simulating a DB migrated by a newer binary.
func newerDB(t *testing.T, dir string) {
	t.Helper()
	ctx := t.Context()
	s, err := Open(ctx, &Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, func(tx *Tx) error {
		_, xerr := tx.Exec("PRAGMA user_version = 9999")
		return xerr
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestReadOnlyNewerSchema (GAP-REL-05): a writer refuses a newer schema with
// ErrSchemaNewer, while a read-only inspection store opens and reads.
func TestReadOnlyNewerSchema(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	newerDB(t, dir)
	if _, err := Open(t.Context(), &Options{Dir: dir, Kind: "run"}); !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("writer open = %v, want ErrSchemaNewer", err)
	}
	s, err := Open(t.Context(), &Options{Dir: dir, Kind: "cli", ReadOnly: true})
	if err != nil {
		t.Fatalf("read-only open: %v", err)
	}
	defer func() {
		if cerr := s.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	if n, err := s.Count(t.Context(), "SELECT count(*) FROM events"); err != nil || n != 0 {
		t.Fatalf("read = %d, %v", n, err)
	}
	if err := s.Write(t.Context(), func(*Tx) error { return nil }); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("write on read-only = %v, want ErrReadOnly", err)
	}
}

func TestReadOnlyMissingDB(t *testing.T) {
	t.Parallel()
	if _, err := Open(t.Context(), &Options{Dir: t.TempDir(), Kind: "cli", ReadOnly: true}); err == nil {
		t.Fatal("read-only open of a missing DB succeeded")
	}
}
