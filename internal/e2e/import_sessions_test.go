package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// copyHermesFixture writes one committed state.db fixture into the temp home.
func copyHermesFixture(t *testing.T, e *Env, name string) {
	t.Helper()
	root, err := os.OpenRoot(filepath.Join("testdata", "hermes"))
	if err != nil {
		t.Fatalf("open fixture root: %v", err)
	}
	data, err := root.ReadFile(name)
	if cerr := root.Close(); cerr != nil {
		t.Fatalf("close fixture root: %v", cerr)
	}
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	home, err := os.OpenRoot(e.home)
	if err != nil {
		t.Fatalf("open home: %v", err)
	}
	defer func() {
		if cerr := home.Close(); cerr != nil {
			t.Errorf("close home: %v", cerr)
		}
	}()
	const rel = ".hermes/state.db"
	if err := home.MkdirAll(filepath.Dir(rel), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := home.WriteFile(rel, data, 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
}

// hashFile hashes one file through os.Root (confined to its directory).
func hashFile(t *testing.T, path string) string {
	t.Helper()
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatalf("open %s: %v", filepath.Dir(path), err)
	}
	data, err := root.ReadFile(filepath.Base(path))
	if cerr := root.Close(); cerr != nil {
		t.Fatalf("close root: %v", cerr)
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func openImportDB(t *testing.T, e *Env) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: importHermesDataDir(e), Kind: "cli"})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close db: %v", cerr)
		}
	})
	return db
}

func countRows(t *testing.T, db *ctxdb.Store, q string) int64 {
	t.Helper()
	n, err := db.Count(t.Context(), q)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func sessionIDByTitle(t *testing.T, db *ctxdb.Store, title string) string {
	t.Helper()
	var id string
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		if qerr := tx.Get(`SELECT session_id FROM session_labels WHERE key = 'title' AND value = ?1
			ORDER BY id DESC LIMIT 1`, []any{title}, &id); qerr != nil {
			return fmt.Errorf("title lookup: %w", qerr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("title %q: %v", title, err)
	}
	return id
}

func importedRoles(t *testing.T, db *ctxdb.Store, sid string) []string {
	t.Helper()
	var roles []string
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		var qerr error
		roles, qerr = tx.Strings(`SELECT role FROM events WHERE session_id = ?1 AND kind = 'imported' ORDER BY seq`, sid)
		if qerr != nil {
			return fmt.Errorf("roles: %w", qerr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("roles: %v", err)
	}
	return roles
}

func firstEventPayload(t *testing.T, db *ctxdb.Store, sid string) string {
	t.Helper()
	var payload string
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		if qerr := tx.Get(`SELECT payload FROM events WHERE session_id = ?1 AND kind = 'imported' ORDER BY seq LIMIT 1`,
			[]any{sid}, &payload); qerr != nil {
			return fmt.Errorf("payload: %w", qerr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	return payload
}

func TestImportHermesSessions(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeHermesHome(t, e)
	sourcePath := filepath.Join(e.home, ".hermes", "state.db")
	copyHermesFixture(t, e, "state.db")
	before := hashFile(t, sourcePath)
	first := importExec(e, "import", "hermes", "--only", "sessions,skills")
	first.RequireOK(t)
	for _, want := range []string{
		"sessions: 2 items, 2 imported, 2 changed, 0 skipped",
		"skills: 1 items, 1 imported, 1 changed, 0 skipped",
	} {
		if !strings.Contains(first.Stdout, want) {
			t.Errorf("import output missing %q:\n%s", want, first.Stdout)
		}
	}
	if hashFile(t, sourcePath) != before {
		t.Errorf("source state.db changed during import")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(sourcePath + suffix); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("source %s created: %v", suffix, err)
		}
	}
	// Rerun: no duplicate session or event.
	again := importExec(e, "import", "hermes", "--only", "sessions")
	again.RequireOK(t)
	if !strings.Contains(again.Stdout, "sessions: 2 items, 2 imported, 0 changed, 0 skipped") {
		t.Errorf("second run not idempotent:\n%s", again.Stdout)
	}
	// A changed source session is a reported skip, never an overwrite.
	copyHermesFixture(t, e, "state-conflict.db")
	conflict := importExec(e, "import", "hermes", "--only", "sessions")
	conflict.RequireOK(t)
	if !strings.Contains(conflict.Stdout, "skipped s1: source session already imported with different content") {
		t.Errorf("conflict not reported:\n%s", conflict.Stdout)
	}
	assertImportedSessions(t, e)
}

// assertImportedSessions checks the destination with no child process running.
func assertImportedSessions(t *testing.T, e *Env) {
	t.Helper()
	db := openImportDB(t, e)
	if n := countRows(t, db, `SELECT count(*) FROM sessions WHERE origin = 'import'`); n != 2 {
		t.Errorf("imported sessions = %d, want 2", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM events WHERE kind = 'imported'`); n != 5 {
		t.Errorf("imported events = %d, want 5", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM events WHERE content = 'changed'`); n != 0 {
		t.Errorf("conflict overwrote %d events", n)
	}
	s1 := sessionIDByTitle(t, db, "First")
	if roles := importedRoles(t, db, s1); strings.Join(roles, ",") != "user,user,assistant" {
		t.Errorf("s1 roles = %v, want timestamp order user,user,assistant", roles)
	}
	if payload := firstEventPayload(t, db, s1); !strings.Contains(payload, `"timestamp_ms":100000`) {
		t.Errorf("payload = %q", payload)
	}
	if s2 := sessionIDByTitle(t, db, "Question about build"); s2 == "" {
		t.Errorf("derived title missing")
	}
}

func TestImportHermesSessionsDryRun(t *testing.T) {
	t.Parallel()
	e := New(t)
	copyHermesFixture(t, e, "state.db")
	before := hashTree(t, e.home)
	res := importExec(e, "import", "hermes", "--only", "sessions", "--dry-run")
	res.RequireOK(t)
	if !strings.Contains(res.Stdout, "sessions: 2 items, 2 imported, 0 changed, 0 skipped") {
		t.Errorf("dry run output:\n%s", res.Stdout)
	}
	if after := hashTree(t, e.home); after != before {
		t.Errorf("dry run changed state: %s -> %s", before, after)
	}
	requireNoDataDir(t, e)
}

func TestImportHermesSessionsBrokenSchema(t *testing.T) {
	t.Parallel()
	e := New(t)
	copyHermesFixture(t, e, "state-broken.db")
	before := hashTree(t, e.home)
	res := importExec(e, "import", "hermes", "--only", "sessions")
	if res.Code == 0 {
		t.Fatalf("broken schema imported, exit 0:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "invalid hermes state.db") {
		t.Errorf("stderr = %q", res.Stderr)
	}
	if after := hashTree(t, e.home); after != before {
		t.Errorf("broken schema changed state")
	}
	requireNoDataDir(t, e)
}

func TestImportHermesSessionsMissingColumn(t *testing.T) {
	t.Parallel()
	e := New(t)
	copyHermesFixture(t, e, "state-missing.db")
	res := importExec(e, "import", "hermes", "--only", "sessions")
	if res.Code == 0 {
		t.Fatalf("missing content column imported, exit 0:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "missing column content") {
		t.Errorf("stderr = %q", res.Stderr)
	}
	requireNoDataDir(t, e)
}

func TestImportHermesSessionsOldSchema(t *testing.T) {
	t.Parallel()
	e := New(t)
	copyHermesFixture(t, e, "state-old.db")
	res := importExec(e, "import", "hermes", "--only", "sessions")
	res.RequireOK(t)
	if !strings.Contains(res.Stdout, "sessions: 1 items, 1 imported, 1 changed, 0 skipped") {
		t.Errorf("old schema output:\n%s", res.Stdout)
	}
	db := openImportDB(t, e)
	if n := countRows(t, db, `SELECT count(*) FROM events WHERE kind = 'imported'`); n != 2 {
		t.Errorf("old schema events = %d, want 2", n)
	}
	if id := sessionIDByTitle(t, db, "from the old schema"); id == "" {
		t.Errorf("old schema title missing")
	}
}
