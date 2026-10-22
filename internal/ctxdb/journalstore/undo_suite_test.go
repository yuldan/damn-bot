package journalstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/journal"
	"github.com/damnbot/damnbot/internal/sandbox"
	"github.com/damnbot/damnbot/internal/testutil/timing"
)

// skipOrFail makes a platform fixture limitation visible in CI (SEC9-10,
// SEC5-25): a would-be skip fails there instead of hiding a missing test.
func skipOrFail(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Fatalf("%s (CI=true; the fixture must work on this runner)", reason)
	}
	t.Skip(reason)
}

// dirLink creates a directory link at link -> target: a symlink on Unix, a
// junction on Windows (no privilege needed).
func dirLink(target, link string) error {
	if err := os.Symlink(target, link); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return fmt.Errorf("symlink: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "cmd", "/c", "mklink", "/J", link, target).CombinedOutput() //nolint:gosec // fixed cmd.exe, temp paths
	if err != nil {
		return fmt.Errorf("junction: %w: %s", err, out)
	}
	return nil
}

func openDB(t *testing.T) (*ctxdb.Store, *Store, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	js, err := New(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	return db, js, dir
}

func session(ctx context.Context, t *testing.T, db *ctxdb.Store, sid string) {
	t.Helper()
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: sid, Origin: "headless", Cwd: "/"}); err != nil {
		t.Fatal(err)
	}
}

func undoer(t *testing.T, js *Store, root string) *journal.Undoer {
	t.Helper()
	// Pass the root's configured spelling: New canonicalizes it and keeps the
	// alias, so paths recorded under the spelling map back (SEC14).
	sb, err := sandbox.New(&sandbox.Config{Roots: []string{root}, Home: root})
	if err != nil {
		t.Fatal(err)
	}
	u, err := journal.NewUndoer(js, sb)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func recordCtx(ctx context.Context, t *testing.T, js *Store, r *journal.Record) int64 {
	t.Helper()
	id, err := js.Record(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func put(t *testing.T, path string, data []byte, mode fs.FileMode) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func content(t *testing.T, path string) []byte {
	t.Helper()
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	f, err := root.Open(filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(f)
	if cerr := errors.Join(f.Close(), root.Close()); cerr != nil {
		t.Fatal(cerr)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func absent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s exists: %v", path, err)
	}
}

func sameMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return // Windows synthesises permission bits; ownership is set at create.
	}
	if got := fi.Mode().Perm(); got != want.Perm() {
		t.Fatalf("%s mode = %#o, want %#o", path, got, want.Perm())
	}
}

func undoneCount(t *testing.T, db *ctxdb.Store, id int64) int64 {
	t.Helper()
	n, err := db.Count(t.Context(), `SELECT count(*) FROM action_undos WHERE action_id = ? AND status = 'undone'`, id)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

type suiteOp string

const (
	opWrite  suiteOp = "write"
	opEdit   suiteOp = "edit"
	opCreate suiteOp = "create"
	opDelete suiteOp = "delete"
	opMove   suiteOp = "move"
)

func bytesRepeat(b []byte, n int) []byte {
	out := make([]byte, 0, len(b)*n)
	for range n {
		out = append(out, b...)
	}
	return out
}

// TestUndoSuite is the F2 acceptance gate: every file-action scenario is
// undone to a byte-identical file (SPEC §18.1, P26). 5 operations × 10
// contents × 4 modes = 200 scenarios, each also checked for idempotence and
// an `undone` action_undos row.
func TestUndoSuite(t *testing.T) {
	ops := []suiteOp{opWrite, opEdit, opCreate, opDelete, opMove}
	contents := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"lf", []byte("alpha\nbeta\n")},
		{"crlf", []byte("alpha\r\nbeta\r\n")},
		{"bom", append([]byte{0xef, 0xbb, 0xbf}, []byte("alpha\n")...)},
		{"binary", []byte{0, 1, 2, 3, 0xff, 0xfe, 0, 0x7f}},
		{"unicode", []byte("héllo — 世界 🌍\n")},
		{"no-eol", []byte("no trailing newline")},
		{"tabs", []byte("a\tb\tc\n\tindented\n")},
		{"json", []byte(`{"a":1,"b":[true,false,null]}`)},
		{"long", bytesRepeat([]byte("0123456789abcdef"), 4096)},
	}
	modes := []fs.FileMode{0o644, 0o755, 0o600, 0o440}
	db, js, _ := openDB(t)
	root := t.TempDir()
	u := undoer(t, js, root)
	seq := 0
	for _, op := range ops {
		for _, c := range contents {
			for _, mode := range modes {
				seq++
				name := fmt.Sprintf("%s/%s/%#o", op, c.name, mode)
				t.Run(name, func(t *testing.T) {
					runUndoScenario(t, db, js, u, root, seq, op, c.data, mode)
				})
			}
		}
	}
	if seq < 100 {
		t.Fatalf("TestUndoSuite scenarios = %d, want ≥ 100", seq)
	}
}

func scenarioRecord(t *testing.T, sid string, op suiteOp, path, dst string, data []byte, mode fs.FileMode, after []byte, afterMode fs.FileMode) *journal.Record {
	t.Helper()
	rec := &journal.Record{SessionID: sid, Kind: kindOf(op), Path: path}
	switch op {
	case opWrite, opEdit:
		put(t, path, data, mode)
		rec.Before, rec.After = &journal.Snapshot{Data: data, Mode: mode}, &journal.Snapshot{Data: after, Mode: afterMode}
	case opCreate:
		rec.After = &journal.Snapshot{Data: after, Mode: afterMode}
	case opDelete:
		put(t, path, data, mode)
		rec.Before = &journal.Snapshot{Data: data, Mode: mode}
	case opMove:
		put(t, path, data, mode)
		rec.MoveTo = dst
		rec.Before = &journal.Snapshot{Data: data, Mode: mode}
		rec.After = &journal.Snapshot{Data: data, Mode: mode}
	default: // unreachable: suiteOp is closed
	}
	return rec
}

func scenarioApply(t *testing.T, op suiteOp, path, dst string, after []byte, afterMode fs.FileMode) {
	t.Helper()
	switch op {
	case opWrite, opEdit, opCreate:
		put(t, path, after, afterMode)
	case opDelete:
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	case opMove:
		if err := os.Rename(path, dst); err != nil {
			t.Fatal(err)
		}
	default: // unreachable: suiteOp is closed
	}
}

func runUndoScenario(t *testing.T, db *ctxdb.Store, js *Store, u *journal.Undoer, root string, seq int,
	op suiteOp, data []byte, mode fs.FileMode,
) {
	t.Helper()
	sid := fmt.Sprintf("suite-%d", seq)
	session(t.Context(), t, db, sid)
	dir := filepath.Join(root, fmt.Sprintf("s%03d", seq))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "file")
	dst := filepath.Join(dir, "moved")

	afterMode := mode ^ 0o040
	after := append(append([]byte{}, data...), []byte("\n--after--\n")...)
	rec := scenarioRecord(t, sid, op, path, dst, data, mode, after, afterMode)
	id := recordCtx(t.Context(), t, js, rec)
	scenarioApply(t, op, path, dst, after, afterMode)

	res, err := u.Undo(t.Context(), sid, id, false)
	if err != nil || res.Status != journal.Undone {
		t.Fatalf("undo = %+v, %v", res, err)
	}
	assertRestored(t, op, path, dst, data, mode)
	if n := undoneCount(t, db, id); n != 1 {
		t.Fatalf("undone rows = %d", n)
	}
	// Idempotent: the second undo is a no-op and records another undone row.
	res, err = u.Undo(t.Context(), sid, id, false)
	if err != nil || res.Status != journal.Undone || res.Detail != "already at the before image" {
		t.Fatalf("second undo = %+v, %v", res, err)
	}
	assertRestored(t, op, path, dst, data, mode)
	if n := undoneCount(t, db, id); n != 2 {
		t.Fatalf("undone rows after retry = %d", n)
	}
}

func kindOf(op suiteOp) journal.Kind {
	switch op {
	case opEdit:
		return journal.FileEdit
	case opDelete:
		return journal.FileDelete
	case opMove:
		return journal.FileMove
	case opWrite, opCreate:
		return journal.FileWrite
	}
	return journal.FileWrite
}

func assertRestored(t *testing.T, op suiteOp, path, dst string, data []byte, mode fs.FileMode) {
	t.Helper()
	switch op {
	case opCreate:
		absent(t, path)
	case opMove:
		if got := content(t, path); !bytes.Equal(got, data) {
			t.Fatalf("source = %q, want %q", got, data)
		}
		sameMode(t, path, mode)
		absent(t, dst)
	case opWrite, opEdit, opDelete:
		if got := content(t, path); !bytes.Equal(got, data) {
			t.Fatalf("content = %q, want %q", got, data)
		}
		sameMode(t, path, mode)
	}
}

// Refusal coverage: every unsafe branch is an explicit, named test.

func refusalEnv(t *testing.T) (*ctxdb.Store, *Store, *journal.Undoer, string, string) {
	t.Helper()
	db, js, dir := openDB(t)
	root := t.TempDir()
	return db, js, undoer(t, js, root), root, dir
}

func refusalDir(t *testing.T, root, name string) string {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(p, 0o750); err != nil {
		t.Fatal(err)
	}
	return p
}

func refusalWrite(ctx context.Context, t *testing.T, js *Store, sid, p string, before, after []byte) int64 {
	t.Helper()
	return recordCtx(ctx, t, js, &journal.Record{
		SessionID: sid, Kind: journal.FileWrite, Path: p,
		Before: &journal.Snapshot{Data: before, Mode: 0o644},
		After:  &journal.Snapshot{Data: after, Mode: 0o644},
	})
}

func TestUndoRefusalCrashBeforeSideEffect(t *testing.T) {
	db, js, u, root, _ := refusalEnv(t)
	session(t.Context(), t, db, "r-crash")
	p := filepath.Join(refusalDir(t, root, "crash"), "f")
	put(t, p, []byte("old"), 0o644)
	id := refusalWrite(t.Context(), t, js, "r-crash", p, []byte("old"), []byte("new"))
	res, err := u.Undo(t.Context(), "r-crash", id, false)
	if err != nil || res.Status != journal.Undone || !bytes.Equal(content(t, p), []byte("old")) {
		t.Fatalf("crash undo = %+v, %v", res, err)
	}
}

func TestUndoRefusalPartialContent(t *testing.T) {
	db, js, u, root, _ := refusalEnv(t)
	session(t.Context(), t, db, "r-partial")
	p := filepath.Join(refusalDir(t, root, "partial"), "f")
	put(t, p, []byte("ol"), 0o644)
	id := refusalWrite(t.Context(), t, js, "r-partial", p, []byte("old"), []byte("new"))
	res, err := u.Undo(t.Context(), "r-partial", id, false)
	if !errors.Is(err, journal.ErrConflict) || res.Status != journal.Conflict || !bytes.Equal(content(t, p), []byte("ol")) {
		t.Fatalf("partial = %+v, %v", res, err)
	}
}

func TestUndoRefusalConflictForce(t *testing.T) {
	db, js, u, root, _ := refusalEnv(t)
	session(t.Context(), t, db, "r-conflict")
	p := filepath.Join(refusalDir(t, root, "conflict"), "f")
	put(t, p, []byte("other"), 0o644)
	id := refusalWrite(t.Context(), t, js, "r-conflict", p, []byte("old"), []byte("new"))
	if _, err := u.Undo(t.Context(), "r-conflict", id, false); !errors.Is(err, journal.ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if !bytes.Equal(content(t, p), []byte("other")) {
		t.Fatal("conflict overwrote the file")
	}
	res, err := u.Undo(t.Context(), "r-conflict", id, true)
	if err != nil || res.Status != journal.Undone || !bytes.Equal(content(t, p), []byte("old")) {
		t.Fatalf("force = %+v, %v", res, err)
	}
}

func TestUndoRefusalOutsideRoot(t *testing.T) {
	db, js, u, _, _ := refusalEnv(t)
	session(t.Context(), t, db, "r-outside")
	p := filepath.Join(t.TempDir(), "f")
	put(t, p, []byte("new"), 0o644)
	id := refusalWrite(t.Context(), t, js, "r-outside", p, []byte("old"), []byte("new"))
	if _, err := u.Undo(t.Context(), "r-outside", id, false); !errors.Is(err, journal.ErrUnsafePath) {
		t.Fatalf("outside: %v", err)
	}
	if !bytes.Equal(content(t, p), []byte("new")) {
		t.Fatal("outside file changed")
	}
}

func TestUndoRefusalSymlinkEscape(t *testing.T) {
	db, js, u, root, _ := refusalEnv(t)
	session(t.Context(), t, db, "r-symlink")
	target := filepath.Join(t.TempDir(), "f")
	put(t, target, []byte("new"), 0o644)
	link := filepath.Join(refusalDir(t, root, "symlink"), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	id := refusalWrite(t.Context(), t, js, "r-symlink", link, []byte("old"), []byte("new"))
	if _, err := u.Undo(t.Context(), "r-symlink", id, false); !errors.Is(err, journal.ErrUnsafePath) {
		t.Fatalf("symlink: %v", err)
	}
	if !bytes.Equal(content(t, target), []byte("new")) {
		t.Fatal("symlink target changed")
	}
}

func TestUndoRefusalExpired(t *testing.T) {
	db, js, _, root, _ := refusalEnv(t)
	session(t.Context(), t, db, "r-expired")
	p := filepath.Join(refusalDir(t, root, "expired"), "f")
	put(t, p, []byte("new"), 0o644)
	id := refusalWrite(t.Context(), t, js, "r-expired", p, []byte("old"), []byte("new"))
	future := time.Now().UnixMilli() + 2*dayMs
	old, err := New(db, &Options{RetentionDays: 1, Now: func() int64 { return future }})
	if err != nil {
		t.Fatal(err)
	}
	ou, err := journal.NewUndoer(old, mustSandbox(t, root))
	if err != nil {
		t.Fatal(err)
	}
	res, err := ou.Undo(t.Context(), "r-expired", id, false)
	if !errors.Is(err, journal.ErrExpired) || res.Status != journal.Expired || !bytes.Equal(content(t, p), []byte("new")) {
		t.Fatalf("expired = %+v, %v", res, err)
	}
}

func TestUndoRefusalOpenTurn(t *testing.T) {
	db, js, u, root, _ := refusalEnv(t)
	session(t.Context(), t, db, "r-open")
	p := filepath.Join(refusalDir(t, root, "open"), "f")
	put(t, p, []byte("new"), 0o644)
	id := recordCtx(t.Context(), t, js, &journal.Record{
		SessionID: "r-open", TurnID: "turn-open", Kind: journal.FileWrite, Path: p,
		Before: &journal.Snapshot{Data: []byte("old"), Mode: 0o644},
		After:  &journal.Snapshot{Data: []byte("new"), Mode: 0o644},
	})
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error { return tx.StartTurn("r-open", "turn-open") }); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Undo(t.Context(), "r-open", id, false); !errors.Is(err, journal.ErrPending) {
		t.Fatalf("open turn: %v", err)
	}
}

func TestUndoRefusalMissingBlob(t *testing.T) {
	db, js, u, root, dir := refusalEnv(t)
	session(t.Context(), t, db, "r-blob")
	p := filepath.Join(refusalDir(t, root, "blob"), "f")
	put(t, p, []byte("new"), 0o644)
	id := refusalWrite(t.Context(), t, js, "r-blob", p, []byte("old-data"), []byte("new"))
	a, err := js.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(dir, "blobs", a.Before.BlobSHA[:2], a.Before.BlobSHA)
	if rerr := os.Remove(blob); rerr != nil {
		t.Fatal(rerr)
	}
	res, err := u.Undo(t.Context(), "r-blob", id, false)
	if !errors.Is(err, journal.ErrBlob) || res.Status != journal.Failed || !bytes.Equal(content(t, p), []byte("new")) {
		t.Fatalf("missing blob = %+v, %v", res, err)
	}
}

func TestUndoRefusalIrreversible(t *testing.T) {
	db, js, u, root, _ := refusalEnv(t)
	session(t.Context(), t, db, "r-irr")
	p := filepath.Join(refusalDir(t, root, "irr"), "f")
	put(t, p, []byte("new"), 0o644)
	base := refusalWrite(t.Context(), t, js, "r-irr", p, []byte("old"), []byte("new"))
	a, err := js.Get(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`INSERT INTO actions(session_id, event_id, kind, reversibility, subject, inverse, created_at)
			VALUES ('r-irr', ?, 'shell', 'partial', '/tmp/x', '{}', ?) RETURNING id`, []any{a.EventID, js.opt.Now()}, &id)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Undo(t.Context(), "r-irr", id, false); !errors.Is(err, journal.ErrIrreversible) {
		t.Fatalf("irreversible: %v", err)
	}
}

func TestUndoRefusalHardlinkCreate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard link detection is unavailable on Windows")
	}
	db, js, u, root, _ := refusalEnv(t)
	session(t.Context(), t, db, "r-hard")
	d := refusalDir(t, root, "hard")
	p, q := filepath.Join(d, "a"), filepath.Join(d, "b")
	put(t, p, []byte("body"), 0o644)
	if err := os.Link(p, q); err != nil {
		t.Skipf("hard link: %v", err)
	}
	id := refusalWrite(t.Context(), t, js, "r-hard", p, nil, []byte("body"))
	if _, err := u.Undo(t.Context(), "r-hard", id, false); !errors.Is(err, journal.ErrUnsafePath) {
		t.Fatalf("hard link: %v", err)
	}
	if !bytes.Equal(content(t, p), []byte("body")) || !bytes.Equal(content(t, q), []byte("body")) {
		t.Fatal("hard link removed without force")
	}
}

func TestUndoRefusalMoveDestinationGone(t *testing.T) {
	db, js, u, root, _ := refusalEnv(t)
	session(t.Context(), t, db, "r-move")
	d := refusalDir(t, root, "move")
	src, dst := filepath.Join(d, "a"), filepath.Join(d, "b")
	put(t, dst, []byte("body"), 0o644)
	id := recordCtx(t.Context(), t, js, &journal.Record{
		SessionID: "r-move", Kind: journal.FileMove, Path: src, MoveTo: dst,
		Before: &journal.Snapshot{Data: []byte("body"), Mode: 0o644},
		After:  &journal.Snapshot{Data: []byte("body"), Mode: 0o644},
	})
	if err := os.Remove(dst); err != nil {
		t.Fatal(err)
	}
	res, err := u.Undo(t.Context(), "r-move", id, false)
	if !errors.Is(err, journal.ErrState) || res.Status != journal.Failed {
		t.Fatalf("move gone = %+v, %v", res, err)
	}
}

func TestUndoRefusalCorruptBlob(t *testing.T) {
	db, js, dir := openDB(t)
	root := t.TempDir()
	u := undoer(t, js, root)
	session(t.Context(), t, db, "r-corrupt")
	p := filepath.Join(root, "f")
	put(t, p, []byte("new"), 0o644)
	id := refusalWrite(t.Context(), t, js, "r-corrupt", p, []byte("old"), []byte("new"))
	a, err := js.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(dir, "blobs", a.Before.BlobSHA[:2], a.Before.BlobSHA)
	r, err := os.OpenRoot(filepath.Dir(blob))
	if err != nil {
		t.Fatal(err)
	}
	f, err := r.OpenFile(filepath.Base(blob), os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, werr := f.WriteString("EVIL")
	werr = errors.Join(werr, f.Close(), r.Close())
	if werr != nil {
		t.Fatal(werr)
	}
	if _, berr := js.Blob(t.Context(), a.Before.BlobSHA); !errors.Is(berr, journal.ErrBlob) {
		t.Fatalf("corrupt blob = %v", berr)
	}
	res, err := u.Undo(t.Context(), "r-corrupt", id, false)
	if !errors.Is(err, journal.ErrBlob) || res.Status != journal.Failed || !bytes.Equal(content(t, p), []byte("new")) {
		t.Fatalf("corrupt undo = %+v, %v", res, err)
	}
}

func TestUndoSuiteUniqueBytes(t *testing.T) {
	db, _, _ := openDB(t)
	now := int64(10 * dayMs)
	timed, err := New(db, &Options{RetentionDays: 30, Now: func() int64 { return now }})
	if err != nil {
		t.Fatal(err)
	}
	session(t.Context(), t, db, "unique")
	shared := []byte("shared")
	first := recordCtx(t.Context(), t, timed, &journal.Record{
		SessionID: "unique", Kind: journal.FileWrite, Path: "/w/a",
		Before: &journal.Snapshot{Data: shared, Mode: 0o644},
		After:  &journal.Snapshot{Data: []byte("a"), Mode: 0o644},
	})
	now += dayMs
	second := recordCtx(t.Context(), t, timed, &journal.Record{
		SessionID: "unique", Kind: journal.FileWrite, Path: "/w/b",
		Before: &journal.Snapshot{Data: shared, Mode: 0o644},
		After:  &journal.Snapshot{Data: []byte("b"), Mode: 0o644},
	})
	cap7, err := New(db, &Options{RetentionDays: 30, MaxBytes: 7, Now: func() int64 { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if a, gerr := cap7.Get(t.Context(), first); gerr != nil || !a.Expired {
		t.Fatalf("first expired = %v, %v", a.Expired, gerr)
	}
	if a, gerr := cap7.Get(t.Context(), second); gerr != nil || a.Expired {
		t.Fatalf("second expired = %v, %v", a.Expired, gerr)
	}
}

func TestUndo50Files(t *testing.T) {
	db, js, _ := openDB(t)
	root := t.TempDir()
	u := undoer(t, js, root)
	session(t.Context(), t, db, "fifty")
	const n = 50
	for i := range n {
		p := filepath.Join(root, fmt.Sprintf("f%02d", i))
		before := []byte(fmt.Sprintf("before-%d", i))
		after := []byte(fmt.Sprintf("after-%d", i))
		put(t, p, after, 0o644)
		recordCtx(t.Context(), t, js, &journal.Record{
			SessionID: "fifty", Kind: journal.FileWrite, Path: p,
			Before: &journal.Snapshot{Data: before, Mode: 0o640},
			After:  &journal.Snapshot{Data: after, Mode: 0o644},
		})
	}
	start := time.Now()
	res, err := u.UndoLast(t.Context(), "fifty", n, false)
	elapsed := time.Since(start)
	t.Logf("undo 50 files: %v", elapsed)
	if err != nil || len(res) != n {
		t.Fatalf("undo 50 = %d, %v", len(res), err)
	}
	// Linux keeps the 200 ms budget. APFS/HFS fsync and Windows file handles
	// are slower on shared runners; timing.Check still gates (ADR-048).
	limit := 200 * time.Millisecond
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		limit = 600 * time.Millisecond
	}
	timing.Check(t, "undo 50 files", elapsed, limit)
	for i := range n {
		p := filepath.Join(root, fmt.Sprintf("f%02d", i))
		if !bytes.Equal(content(t, p), []byte(fmt.Sprintf("before-%d", i))) {
			t.Fatalf("%s not restored", p)
		}
		sameMode(t, p, 0o640)
	}
}

func mustSandbox(t *testing.T, root string) *sandbox.Sandbox {
	t.Helper()
	sb, err := sandbox.New(&sandbox.Config{Roots: []string{root}, Home: root})
	if err != nil {
		t.Fatal(err)
	}
	return sb
}

// TestUndoSuiteMultiFile: undo N walks the newest actions first and restores
// every file byte-identically.
func TestUndoSuiteMultiFile(t *testing.T) {
	db, js, _ := openDB(t)
	root := t.TempDir()
	u := undoer(t, js, root)
	session(t.Context(), t, db, "multi")
	var ids []int64
	for i := range 3 {
		p := filepath.Join(root, fmt.Sprintf("m%d", i))
		before := []byte(strings.Repeat(fmt.Sprintf("before-%d\n", i), 3))
		after := []byte(strings.Repeat(fmt.Sprintf("after-%d\n", i), 3))
		put(t, p, after, 0o644)
		ids = append(ids, recordCtx(t.Context(), t, js, &journal.Record{
			SessionID: "multi", Kind: journal.FileWrite, Path: p,
			Before: &journal.Snapshot{Data: before, Mode: 0o600},
			After:  &journal.Snapshot{Data: after, Mode: 0o644},
		}))
	}
	res, err := u.UndoLast(t.Context(), "multi", 3, false)
	if err != nil || len(res) != 3 {
		t.Fatalf("undo last = %+v, %v", res, err)
	}
	for i := range 3 {
		if res[i].ActionID != ids[2-i] {
			t.Fatalf("order[%d] = %d, want %d", i, res[i].ActionID, ids[2-i])
		}
		p := filepath.Join(root, fmt.Sprintf("m%d", i))
		want := strings.Repeat(fmt.Sprintf("before-%d\n", i), 3)
		if string(content(t, p)) != want {
			t.Fatalf("%s = %q, want %q", p, content(t, p), want)
		}
		sameMode(t, p, 0o600)
	}
}

// TestUndoRootSymlinkForm: the journaled path may use the workspace root's
// configured spelling (on macOS /var is a symlink to /private/var) while the
// sandbox canonicalizes the root. Only the root prefix may differ; undo must
// still restore the file (SEC14 root canonicalization).
func TestUndoRootSymlinkForm(t *testing.T) {
	db, js, _ := openDB(t)
	realRoot := t.TempDir()
	linkRoot := filepath.Join(t.TempDir(), "var")
	if err := dirLink(realRoot, linkRoot); err != nil {
		skipOrFail(t, "directory link: "+err.Error())
	}
	u := undoer(t, js, linkRoot) // sandbox root is the canonical /private/... form
	session(t.Context(), t, db, "r-rootlink")
	p := filepath.Join(linkRoot, "f")
	put(t, p, []byte("new"), 0o644)
	res, err := u.Undo(t.Context(), "r-rootlink", refusalWrite(t.Context(), t, js, "r-rootlink", p, []byte("old"), []byte("new")), false)
	if err != nil || res.Status != journal.Undone || !bytes.Equal(content(t, p), []byte("old")) {
		t.Fatalf("root symlink undo = %+v, %v", res, err)
	}
}

// TestUndoRootAliasInternalSymlinkRefused: mapping the root's own spelling must
// not accept a symlink below the root. The link is inside the workspace and is
// still refused.
func TestUndoRootAliasInternalSymlinkRefused(t *testing.T) {
	db, js, _ := openDB(t)
	realRoot := t.TempDir()
	linkRoot := filepath.Join(t.TempDir(), "var")
	if err := dirLink(realRoot, linkRoot); err != nil {
		skipOrFail(t, "directory link: "+err.Error())
	}
	u := undoer(t, js, linkRoot)
	session(t.Context(), t, db, "r-internal")
	d := refusalDir(t, realRoot, "d")
	realDir := filepath.Join(d, "real")
	if err := os.MkdirAll(realDir, 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(realDir, "note.txt")
	put(t, target, []byte("new"), 0o644)
	link := filepath.Join(d, "link")
	if err := dirLink(realDir, link); err != nil {
		skipOrFail(t, "directory link: "+err.Error())
	}
	p := filepath.Join(linkRoot, "d", "link", "note.txt")
	res, err := u.Undo(t.Context(), "r-internal", refusalWrite(t.Context(), t, js, "r-internal", p, []byte("old"), []byte("new")), false)
	if !errors.Is(err, journal.ErrConflict) || res.Status != journal.Conflict {
		t.Fatalf("internal symlink = %+v, %v, want conflict", res, err)
	}
	if !bytes.Equal(content(t, target), []byte("new")) {
		t.Fatal("internal symlink target changed")
	}
}
