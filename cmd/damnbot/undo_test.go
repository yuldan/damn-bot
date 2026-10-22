package main

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func undoCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(append([]string{"undo"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

// wantUndo runs `undo args` and requires the exit code and stdout substrings.
func wantUndo(t *testing.T, code int, args []string, subs ...string) string {
	t.Helper()
	got, out, errOut := undoCLI(t, args...)
	if got != code {
		t.Fatalf("undo %q: exit %d, want %d; stdout %q stderr %q", args, got, code, out, errOut)
	}
	for _, s := range subs {
		if !strings.Contains(out, s) {
			t.Fatalf("undo %q: stdout %q lacks %q", args, out, s)
		}
	}
	return out
}

// undoRows counts undone action_undos recorded in the action's own session
// and the session leases left behind.
func undoRows(t *testing.T, data string) (undone, leases int64) {
	t.Helper()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: data, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	err = db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		if gerr := tx.Get(`SELECT count(*) FROM action_undos u JOIN actions a ON a.id = u.action_id
			JOIN events e ON e.id = u.event_id WHERE u.status = 'undone' AND e.session_id = a.session_id`, nil, &undone); gerr != nil {
			return fmt.Errorf("count undos: %w", gerr)
		}
		if gerr := tx.Get("SELECT count(*) FROM session_leases", nil, &leases); gerr != nil {
			return fmt.Errorf("count leases: %w", gerr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return undone, leases
}

// TestUndoRestoresEditByteIdentical: run journals the edit before writing;
// `undo ls`/`--dry-run` change nothing; `undo` restores bytes and mode,
// records the outcome under a short lease and releases it (SPEC §18.1).
func TestUndoRestoresEditByteIdentical(t *testing.T) {
	orig := []byte("\xef\xbb\xbfalpha\r\nbeta\r\n")
	ws, data := undoWorkspace(t, orig, 0o400)
	p := filepath.Join(ws, "f.txt")
	edited := readFile(t, p)
	if out := wantUndo(t, 0, []string{"ls", "--data-dir", data}, "file_edit "+p); !strings.HasPrefix(out, "a") {
		t.Fatalf("ls rows lack the a<id> form: %q", out)
	}
	wantUndo(t, 0, []string{"--dry-run", "--data-dir", data}, "would restore the before image", "-gamma", "+beta")
	if !bytes.Equal(readFile(t, p), edited) {
		t.Fatal("dry-run touched the file")
	}
	wantUndo(t, 0, []string{"1", "--data-dir", data}, " undone ")
	if got := readFile(t, p); !bytes.Equal(got, orig) {
		t.Fatalf("undo bytes = %q, want %q", got, orig)
	}
	// Windows models only the read-only bit, so a 0o400 file reads back as
	// 0o444 there while POSIX keeps the exact mode.
	want := fs.FileMode(0o400)
	if runtime.GOOS == "windows" {
		want = 0o444
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
		t.Fatalf("mode after undo: %v %v", fi, err)
	}
	if undone, leases := undoRows(t, data); undone != 1 || leases != 0 {
		t.Fatalf("undone rows = %d, leases = %d", undone, leases)
	}
}

// TestUndoConflictRefuses: a file changed after the action is a conflict
// (exit 1, untouched) until --force restores the before image.
func TestUndoConflictRefuses(t *testing.T) {
	orig := []byte("alpha\nbeta\n")
	ws, data := undoWorkspace(t, orig, 0o600)
	p := filepath.Join(ws, "f.txt")
	user := []byte("alpha\ngamma\nuser line\n")
	if err := os.WriteFile(p, user, 0o600); err != nil {
		t.Fatal(err)
	}
	wantUndo(t, 0, []string{"--dry-run", "--data-dir", data}, "conflict", "+user line")
	wantUndo(t, 1, []string{"--data-dir", data}, " conflict ")
	if got := readFile(t, p); !bytes.Equal(got, user) {
		t.Fatalf("conflict overwrote the file: %q", got)
	}
	wantUndo(t, 0, []string{"--force", "--data-dir", data}, " undone ")
	if got := readFile(t, p); !bytes.Equal(got, orig) {
		t.Fatalf("undo --force bytes = %q, want %q", got, orig)
	}
}

// TestUndoParseErrors: N, a<id> and ls are the only targets.
func TestUndoParseErrors(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"0"}, {"a0"}, {"abc"}, {"1", "2"}, {"-nope"}} {
		var out, errb bytes.Buffer
		if code := run(append([]string{"undo"}, args...), &out, &errb); code != 2 || out.Len() != 0 {
			t.Errorf("undo %q: exit %d stdout %q", args, code, out.String())
		}
	}
	o, err := parseUndoFlags([]string{"a7", "--dry-run"}, &bytes.Buffer{})
	if err != nil || o.action != 7 || !o.dryRun {
		t.Fatalf("a7 --dry-run = %+v, %v", o, err)
	}
}
