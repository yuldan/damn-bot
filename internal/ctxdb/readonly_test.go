package ctxdb

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

// TestReadOnlyMissingDirCreatesNothing (SEC9-1): a missing data dir is not
// created by an inspection open.
func TestReadOnlyMissingDirCreatesNothing(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "missing")
	if _, err := Open(t.Context(), &Options{Dir: dir, Kind: "cli", ReadOnly: true}); err == nil {
		t.Fatal("read-only open of a missing dir succeeded")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("inspection open created %s: %v", dir, err)
	}
}

// TestReadOnlyOnReadOnlyMedia (SEC9-1): a DB in a 0500 dir with 0400 file is
// readable without layout creation.
func TestReadOnlyOnReadOnlyMedia(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := Open(t.Context(), &Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if werr := s.Write(t.Context(), func(tx *Tx) error {
		_, xerr := tx.Exec("INSERT INTO sessions(id, origin, cwd, model, config_sha, context_tier, created_at) VALUES ('s', 'headless', '/', '', '', '', 1)")
		return xerr
	}); werr != nil {
		t.Fatal(werr)
	}
	if cerr := s.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	for _, name := range []string{"blobs", "backups"} {
		if rerr := os.RemoveAll(filepath.Join(dir, name)); rerr != nil {
			t.Fatal(rerr)
		}
	}
	dbFile := filepath.Join(dir, dbFile)
	if merr := os.Chmod(dbFile, 0o400); merr != nil {
		t.Fatal(merr)
	}
	if merr := os.Chmod(dir, 0o500); merr != nil { //nolint:gosec // fixture: read-only media
		t.Fatal(merr)
	}
	t.Cleanup(func() {
		if merr := os.Chmod(dir, 0o700); merr != nil { //nolint:gosec // restore test dir
			t.Errorf("restore mode: %v", merr)
		}
	})
	for _, name := range []string{"blobs", "backups"} {
		if _, serr := os.Stat(filepath.Join(dir, name)); serr == nil {
			t.Fatalf("inspection created %s", name)
		}
	}
	ro, err := Open(t.Context(), &Options{Dir: dir, Kind: "cli", ReadOnly: true})
	if err != nil {
		t.Fatalf("read-only media open: %v", err)
	}
	defer func() {
		if cerr := ro.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	if n, cerr := ro.Count(t.Context(), "SELECT count(*) FROM sessions WHERE id = 's'"); cerr != nil || n != 1 {
		t.Fatalf("read = %d, %v", n, cerr)
	}
}

// TestReadOnlyDSNPaths (SEC9-4): '#', '?', and '%' in the data dir stay in
// the path and cannot drop mode=ro/query_only.
func TestReadOnlyDSNPaths(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	dir := filepath.Join(base, "a#b%c")
	if runtime.GOOS != "windows" { // '?' is not a legal Windows path character
		dir = filepath.Join(base, "a#b%c?d")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), &Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if cerr := s.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if _, serr := os.Stat(filepath.Join(dir, dbFile)); serr != nil {
		t.Fatalf("writer did not create the DB inside the data dir: %v", serr)
	}
	if _, serr := os.Stat(filepath.Join(base, "a")); serr == nil {
		t.Fatalf("writer created a truncated path under %s", base)
	}
	ro, err := Open(t.Context(), &Options{Dir: dir, Kind: "cli", ReadOnly: true})
	if err != nil {
		t.Fatalf("read-only open: %v", err)
	}
	defer func() {
		if cerr := ro.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	if err := ro.Write(t.Context(), func(*Tx) error { return nil }); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("query_only dropped: %v", err)
	}
}

// TestReadOnlyRefusesAttachVacuum (SEC9-3): the exported free-SQL surface
// cannot create another database file from the read pool.
func TestReadOnlyRefusesAttachVacuum(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	newerDB(t, dir)
	s, err := Open(t.Context(), &Options{Dir: dir, Kind: "cli", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := s.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	target := filepath.Join(t.TempDir(), "out.db")
	for _, q := range []string{
		"VACUUM INTO '" + target + "'",
		"ATTACH DATABASE '" + target + "' AS x",
	} {
		if _, err := s.Count(t.Context(), q); !errors.Is(err, ErrReadOnly) {
			t.Errorf("%q: err = %v, want ErrReadOnly", q, err)
		}
		if _, serr := os.Stat(target); serr == nil {
			t.Fatalf("%q created %s", q, target)
		}
	}
}
