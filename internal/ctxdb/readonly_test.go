package ctxdb

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

// skipOrFail makes a platform fixture limitation visible in CI: a would-be
// skip fails there instead of hiding a missing test (SEC9-10).
func skipOrFail(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Fatalf("%s (CI=true; the fixture must work on this runner)", reason)
	}
	t.Skip(reason)
}

// readOnlyFixture creates a closed store, runs prepare, then makes the media
// read-only. It fails in CI if the runner cannot make the dir unwritable.
func readOnlyFixture(t *testing.T, prepare func(dir string)) string {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(t.Context(), &Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if cerr := s.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if prepare != nil {
		prepare(dir)
	}
	makeFileReadOnly(t, filepath.Join(dir, dbFile))
	makeDirReadOnly(t, dir)
	if dirWritable(dir) {
		skipOrFail(t, "runner could not make the data dir unwritable")
	}
	return dir
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
	makeFileReadOnly(t, dbFile)
	makeDirReadOnly(t, dir)
	if dirWritable(dir) {
		skipOrFail(t, "runner could not make the data dir unwritable")
	}
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

// TestReadOnlyNoFallbackWithLiveHolder (SEC9-8): immutable=1 is only for a
// genuinely read-only dir with no process that may still write.
func TestReadOnlyNoFallbackWithLiveHolder(t *testing.T) {
	t.Parallel()
	// Positive control: the same read-only fixture without a holder opens via
	// the immutable fallback, so the negative assertion is meaningful.
	pos := readOnlyFixture(t, nil)
	if ro, err := Open(t.Context(), &Options{Dir: pos, Kind: "cli", ReadOnly: true}); err != nil {
		t.Fatalf("positive control: immutable fallback should open: %v", err)
	} else if cerr := ro.Close(); cerr != nil {
		t.Errorf("close: %v", cerr)
	}
	dir := readOnlyFixture(t, func(dir string) {
		hb, jerr := json.Marshal(holderInfo{Ident: SelfIdent(), Kind: "run"})
		if jerr != nil {
			t.Fatal(jerr)
		}
		if merr := os.MkdirAll(filepath.Join(dir, holdersDir), 0o700); merr != nil {
			t.Fatal(merr)
		}
		if werr := os.WriteFile(filepath.Join(dir, holdersDir, strconv.Itoa(os.Getpid())), hb, 0o600); werr != nil {
			t.Fatal(werr)
		}
	})
	id := SelfIdent()
	id.PID, id.Start = os.Getpid()+1, "other"
	ro, err := Open(t.Context(), &Options{Dir: dir, Kind: "cli", ReadOnly: true, Ident: &id})
	if err == nil {
		if cerr := ro.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
		t.Fatal("immutable fallback ran with a live holder")
	}
	if !strings.Contains(err.Error(), "copy the data dir") || !strings.Contains(err.Error(), "holder") {
		t.Fatalf("refusal does not explain the holder: %v", err)
	}
}

// TestReadOnlySQLGate (SEC9-7): the gate tokenizes, so names/literals/comments
// are not keyword hits while real DML/PRAGMA/ATTACH and multi-statement SQL
// are rejected.
func TestReadOnlySQLGate(t *testing.T) {
	t.Parallel()
	allowed := []string{
		"SELECT created_at FROM sessions",
		"SELECT * FROM pragma_table_info('sessions')",
		"VALUES (1)",
		"-- comment\nSELECT 1",
		"/* comment */ SELECT 1",
		"SELECT 'DELETE FROM x'",
		`SELECT "update" FROM sessions`,
		"SELECT 1;",
		"WITH x AS (SELECT 1) SELECT * FROM x",
	}
	for _, q := range allowed {
		if !readOnlyQuery(q) {
			t.Errorf("readOnlyQuery(%q) = false, want true", q)
		}
	}
	denied := []string{
		"INSERT INTO x VALUES (1)",
		"PRAGMA table_info(x)",
		"ATTACH DATABASE 'x' AS y",
		"VACUUM INTO 'x'",
		"SELECT 1; DELETE FROM t",
		"CREATE TABLE x(a)",
		"SELECT * FROM t; PRAGMA wal_checkpoint",
	}
	for _, q := range denied {
		if readOnlyQuery(q) {
			t.Errorf("readOnlyQuery(%q) = true, want false", q)
		}
	}
}

// TestReadOnlyMalformedHolderFailsClosed (SEC9-8): an unreadable holder
// record is uncertainty, so the immutable fallback must not run.
func TestReadOnlyMalformedHolderFailsClosed(t *testing.T) {
	t.Parallel()
	// Positive control (SEC9-9): fallback works on this fixture.
	pos := readOnlyFixture(t, nil)
	if ro, err := Open(t.Context(), &Options{Dir: pos, Kind: "cli", ReadOnly: true}); err != nil {
		t.Fatalf("positive control: immutable fallback should open: %v", err)
	} else if cerr := ro.Close(); cerr != nil {
		t.Errorf("close: %v", cerr)
	}
	dir := readOnlyFixture(t, func(dir string) {
		if merr := os.MkdirAll(filepath.Join(dir, holdersDir), 0o700); merr != nil {
			t.Fatal(merr)
		}
		if werr := os.WriteFile(filepath.Join(dir, holdersDir, "corrupt"), []byte("{"), 0o600); werr != nil {
			t.Fatal(werr)
		}
	})
	if ro, oerr := Open(t.Context(), &Options{Dir: dir, Kind: "cli", ReadOnly: true}); oerr == nil {
		if cerr := ro.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
		t.Fatal("immutable fallback ran with an unreadable holder record")
	} else if !strings.Contains(oerr.Error(), "copy the data dir") {
		t.Fatalf("refusal lacks the recovery hint: %v", oerr)
	}
}
