package journalstore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/journal"
	"github.com/damnbot/damnbot/internal/sandbox"
)

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

func session(t *testing.T, db *ctxdb.Store, sid string) {
	t.Helper()
	if _, err := db.CreateSession(t.Context(), ctxdb.Session{ID: sid, Origin: "headless", Cwd: "/"}); err != nil {
		t.Fatal(err)
	}
}

func undoer(t *testing.T, js *Store, root string) *journal.Undoer {
	t.Helper()
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := sandbox.New(&sandbox.Config{Roots: []string{r}, Home: r})
	if err != nil {
		t.Fatal(err)
	}
	u, err := journal.NewUndoer(js, sb)
	if err != nil {
		t.Fatal(err)
	}
	return u
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
	b, err := os.ReadFile(path)
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

var suiteOps = []suiteOp{opWrite, opEdit, opCreate, opDelete, opMove}

var suiteContents = []struct {
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

func bytesRepeat(b []byte, n int) []byte {
	out := make([]byte, 0, len(b)*n)
	for range n {
		out = append(out, b...)
	}
	return out
}

var suiteModes = []fs.FileMode{0o644, 0o755, 0o600, 0o440}

// TestUndoSuite is the F2 acceptance gate: every file-action scenario is
// undone to a byte-identical file (SPEC §18.1, P26). 5 operations × 10
// contents × 4 modes = 200 scenarios, each also checked for idempotence and
// an `undone` action_undos row.
func TestUndoSuite(t *testing.T) {
	db, js, _ := openDB(t)
	root := t.TempDir()
	u := undoer(t, js, root)
	seq := 0
	for _, op := range suiteOps {
		for _, c := range suiteContents {
			for _, mode := range suiteModes {
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

func runUndoScenario(t *testing.T, db *ctxdb.Store, js *Store, u *journal.Undoer, root string, seq int,
	op suiteOp, data []byte, mode fs.FileMode,
) {
	t.Helper()
	sid := fmt.Sprintf("suite-%d", seq)
	session(t, db, sid)
	dir := filepath.Join(root, fmt.Sprintf("s%03d", seq))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "file")
	dst := filepath.Join(dir, "moved")

	afterMode := mode ^ 0o040
	after := append(append([]byte{}, data...), []byte("\n--after--\n")...)
	rec := journal.Record{SessionID: sid, Kind: kindOf(op), Path: path}
	switch op {
	case opWrite, opEdit:
		put(t, path, data, mode)
		rec.Kind, rec.Before, rec.After = kindOf(op), &journal.Snapshot{Data: data, Mode: mode}, &journal.Snapshot{Data: after, Mode: afterMode}
	case opCreate:
		rec.Before, rec.After = nil, &journal.Snapshot{Data: after, Mode: afterMode}
	case opDelete:
		put(t, path, data, mode)
		rec.Before, rec.After = &journal.Snapshot{Data: data, Mode: mode}, nil
	case opMove:
		put(t, path, data, mode)
		rec.MoveTo = dst
		rec.Before = &journal.Snapshot{Data: data, Mode: mode}
		rec.After = &journal.Snapshot{Data: data, Mode: mode}
	}
	id := record(t, js, rec)

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
	}

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
	}
	return journal.FileWrite
}

func assertRestored(t *testing.T, op suiteOp, path, dst string, data []byte, mode fs.FileMode) {
	t.Helper()
	switch op {
	case opCreate:
		absent(t, path)
	case opMove:
		if got := content(t, path); string(got) != string(data) {
			t.Fatalf("source = %q, want %q", got, data)
		}
		sameMode(t, path, mode)
		absent(t, dst)
	default:
		if got := content(t, path); string(got) != string(data) {
			t.Fatalf("content = %q, want %q", got, data)
		}
		sameMode(t, path, mode)
	}
}

// TestUndoSuiteRefusals covers the unsafe branches: crash before the side
// effect, partial content, conflicts, sandbox escapes, expiry, open turns,
// missing blobs, irreversible actions and multi-file undo.
func TestUndoSuiteRefusals(t *testing.T) {
	db, js, dir := openDB(t)
	root := t.TempDir()
	u := undoer(t, js, root)
	ctx := t.Context()
	newDir := func(name string) string {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	writeRec := func(sid, p string, before, after []byte) int64 {
		return record(t, js, journal.Record{
			SessionID: sid, Kind: journal.FileWrite, Path: p,
			Before: &journal.Snapshot{Data: before, Mode: 0o644},
			After:  &journal.Snapshot{Data: after, Mode: 0o644},
		})
	}

	t.Run("crash-before-side-effect", func(t *testing.T) {
		session(t, db, "r-crash")
		p := filepath.Join(newDir("crash"), "f")
		put(t, p, []byte("old"), 0o644)
		id := writeRec("r-crash", p, []byte("old"), []byte("new"))
		res, err := u.Undo(ctx, "r-crash", id, false)
		if err != nil || res.Status != journal.Undone || string(content(t, p)) != "old" {
			t.Fatalf("crash undo = %+v, %v", res, err)
		}
	})

	t.Run("partial-content-conflict", func(t *testing.T) {
		session(t, db, "r-partial")
		p := filepath.Join(newDir("partial"), "f")
		put(t, p, []byte("ol"), 0o644) // half of the before image
		id := writeRec("r-partial", p, []byte("old"), []byte("new"))
		res, err := u.Undo(ctx, "r-partial", id, false)
		if !errors.Is(err, journal.ErrConflict) || res.Status != journal.Conflict || string(content(t, p)) != "ol" {
			t.Fatalf("partial = %+v, %v", res, err)
		}
	})

	t.Run("changed-after-action-conflict-force", func(t *testing.T) {
		session(t, db, "r-conflict")
		p := filepath.Join(newDir("conflict"), "f")
		put(t, p, []byte("other"), 0o644)
		id := writeRec("r-conflict", p, []byte("old"), []byte("new"))
		if _, err := u.Undo(ctx, "r-conflict", id, false); !errors.Is(err, journal.ErrConflict) {
			t.Fatalf("conflict: %v", err)
		}
		if string(content(t, p)) != "other" {
			t.Fatal("conflict overwrote the file")
		}
		res, err := u.Undo(ctx, "r-conflict", id, true)
		if err != nil || res.Status != journal.Undone || string(content(t, p)) != "old" {
			t.Fatalf("force = %+v, %v", res, err)
		}
	})

	t.Run("outside-root-refused", func(t *testing.T) {
		session(t, db, "r-outside")
		outside := t.TempDir()
		p := filepath.Join(outside, "f")
		put(t, p, []byte("new"), 0o644)
		id := writeRec("r-outside", p, []byte("old"), []byte("new"))
		if _, err := u.Undo(ctx, "r-outside", id, false); !errors.Is(err, journal.ErrUnsafePath) {
			t.Fatalf("outside: %v", err)
		}
		if string(content(t, p)) != "new" {
			t.Fatal("outside file changed")
		}
	})

	t.Run("symlink-escape-refused", func(t *testing.T) {
		session(t, db, "r-symlink")
		outside := t.TempDir()
		target := filepath.Join(outside, "f")
		put(t, target, []byte("new"), 0o644)
		link := filepath.Join(newDir("symlink"), "link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink: %v", err)
		}
		id := writeRec("r-symlink", link, []byte("old"), []byte("new"))
		if _, err := u.Undo(ctx, "r-symlink", id, false); !errors.Is(err, journal.ErrUnsafePath) {
			t.Fatalf("symlink: %v", err)
		}
		if string(content(t, target)) != "new" {
			t.Fatal("symlink target changed")
		}
	})

	t.Run("expired", func(t *testing.T) {
		session(t, db, "r-expired")
		p := filepath.Join(newDir("expired"), "f")
		put(t, p, []byte("new"), 0o644)
		id := writeRec("r-expired", p, []byte("old"), []byte("new"))
		future := time.Now().UnixMilli() + 2*dayMs
		old, err := New(db, &Options{RetentionDays: 1, Now: func() int64 { return future }})
		if err != nil {
			t.Fatal(err)
		}
		ou, err := journal.NewUndoer(old, mustSandbox(t, root))
		if err != nil {
			t.Fatal(err)
		}
		res, err := ou.Undo(ctx, "r-expired", id, false)
		if !errors.Is(err, journal.ErrExpired) || res.Status != journal.Expired || string(content(t, p)) != "new" {
			t.Fatalf("expired = %+v, %v", res, err)
		}
	})

	t.Run("open-turn-refused", func(t *testing.T) {
		session(t, db, "r-open")
		p := filepath.Join(newDir("open"), "f")
		put(t, p, []byte("new"), 0o644)
		id := record(t, js, journal.Record{
			SessionID: "r-open", TurnID: "turn-open", Kind: journal.FileWrite, Path: p,
			Before: &journal.Snapshot{Data: []byte("old"), Mode: 0o644},
			After:  &journal.Snapshot{Data: []byte("new"), Mode: 0o644},
		})
		if err := db.Write(ctx, func(tx *ctxdb.Tx) error { return tx.StartTurn("r-open", "turn-open") }); err != nil {
			t.Fatal(err)
		}
		if _, err := u.Undo(ctx, "r-open", id, false); !errors.Is(err, journal.ErrPending) {
			t.Fatalf("open turn: %v", err)
		}
	})

	t.Run("missing-before-blob", func(t *testing.T) {
		session(t, db, "r-blob")
		p := filepath.Join(newDir("blob"), "f")
		put(t, p, []byte("new"), 0o644)
		id := writeRec("r-blob", p, []byte("old-data"), []byte("new"))
		a, err := js.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		blob := filepath.Join(dir, "blobs", a.Before.BlobSHA[:2], a.Before.BlobSHA)
		if err := os.Remove(blob); err != nil {
			t.Fatal(err)
		}
		res, err := u.Undo(ctx, "r-blob", id, false)
		if !errors.Is(err, journal.ErrBlob) || res.Status != journal.Failed || string(content(t, p)) != "new" {
			t.Fatalf("missing blob = %+v, %v", res, err)
		}
	})

	t.Run("irreversible", func(t *testing.T) {
		session(t, db, "r-irr")
		p := filepath.Join(newDir("irr"), "f")
		put(t, p, []byte("new"), 0o644)
		base := writeRec("r-irr", p, []byte("old"), []byte("new"))
		a, err := js.Get(ctx, base)
		if err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
			return tx.Get(`INSERT INTO actions(session_id, event_id, kind, reversibility, subject, inverse, created_at)
				VALUES ('r-irr', ?, 'shell', 'partial', '/tmp/x', '{}', ?) RETURNING id`, []any{a.EventID, js.opt.Now()}, &id)
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := u.Undo(ctx, "r-irr", id, false); !errors.Is(err, journal.ErrIrreversible) {
			t.Fatalf("irreversible: %v", err)
		}
	})

	t.Run("hardlink", func(t *testing.T) {
		session(t, db, "r-hard")
		d := newDir("hard")
		p, q := filepath.Join(d, "a"), filepath.Join(d, "b")
		put(t, p, []byte("new"), 0o644)
		if err := os.Link(p, q); err != nil {
			t.Skipf("hard link: %v", err)
		}
		id := writeRec("r-hard", p, []byte("old"), []byte("new"))
		if _, err := u.Undo(ctx, "r-hard", id, false); !errors.Is(err, journal.ErrUnsafePath) {
			t.Fatalf("hard link: %v", err)
		}
		if _, err := u.Undo(ctx, "r-hard", id, true); err != nil {
			t.Fatal(err)
		}
		if string(content(t, p)) != "old" || string(content(t, q)) != "old" {
			t.Fatal("hard link not rewritten in place")
		}
	})

	t.Run("move-destination-gone", func(t *testing.T) {
		session(t, db, "r-move")
		d := newDir("move")
		src, dst := filepath.Join(d, "a"), filepath.Join(d, "b")
		put(t, dst, []byte("body"), 0o644)
		id := record(t, js, journal.Record{
			SessionID: "r-move", Kind: journal.FileMove, Path: src, MoveTo: dst,
			Before: &journal.Snapshot{Data: []byte("body"), Mode: 0o644},
			After:  &journal.Snapshot{Data: []byte("body"), Mode: 0o644},
		})
		if err := os.Remove(dst); err != nil {
			t.Fatal(err)
		}
		res, err := u.Undo(ctx, "r-move", id, false)
		if !errors.Is(err, journal.ErrState) || res.Status != journal.Failed {
			t.Fatalf("move gone = %+v, %v", res, err)
		}
	})
}

func mustSandbox(t *testing.T, root string) *sandbox.Sandbox {
	t.Helper()
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := sandbox.New(&sandbox.Config{Roots: []string{r}, Home: r})
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
	session(t, db, "multi")
	var ids []int64
	for i := range 3 {
		p := filepath.Join(root, fmt.Sprintf("m%d", i))
		before := []byte(strings.Repeat(fmt.Sprintf("before-%d\n", i), 3))
		after := []byte(strings.Repeat(fmt.Sprintf("after-%d\n", i), 3))
		put(t, p, after, 0o644)
		ids = append(ids, record(t, js, journal.Record{
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
