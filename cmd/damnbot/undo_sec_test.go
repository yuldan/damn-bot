package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/journalstore"
	"github.com/damnbot/damnbot/internal/journal"
)

// seedAction journals a write of path (before → after) in session sid
// (created with cwd) under the sandbox roots; nil roots journal no sandbox
// (a pre-fix action). It returns the action id.
func seedAction(t *testing.T, data, sid, cwd, path string, before, after []byte, roots []string) int64 {
	t.Helper()
	ctx := context.Background()
	db := openData(t, data)
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: sid, Origin: "headless", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	var scope json.RawMessage
	if roots != nil {
		var err error
		if scope, err = journalScope(roots, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	store, err := journalstore.New(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.Record(ctx, &journal.Record{
		SessionID: sid, ToolName: "write_file", Kind: journal.FileWrite, Path: path, Scope: scope,
		Before: &journal.Snapshot{Data: before, Mode: 0o600}, After: &journal.Snapshot{Data: after, Mode: 0o600},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ReleaseLease(ctx, sid); err != nil {
		t.Fatal(err)
	}
	return id
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func aid(id int64) string { return "a" + strconv.FormatInt(id, 10) }

// TestUndoJournaledSandbox (REVIEW-F2-SEC5-1): undo rebuilds only the
// sandbox journaled with the action. A tampered session cwd ("/") and a
// widened add_dirs cannot make an outside path undoable; an action without
// a journaled sandbox is refused; a path denied by the current config stays
// denied.
func TestUndoJournaledSandbox(t *testing.T) {
	ws, other, data := realDir(t), realDir(t), t.TempDir()
	t.Chdir(ws)
	out := filepath.Join(other, "x.txt")
	if err := os.WriteFile(out, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedAction(t, data, "S-out", "/", out, []byte("old"), []byte("new"), []string{ws})
	wide := writeConfig(t, "[approval]\nadd_dirs = [\""+other+"\", \"/\"]\n")
	wantUndo(t, 1, []string{"--session", "S-out", "--config", wide, "--data-dir", data})
	if got := readFile(t, out); string(got) != "new" {
		t.Fatalf("outside file restored through a widened sandbox: %q", got)
	}
	if o := wantUndo(t, 0, []string{"--dry-run", "--session", "S-out", "--config", wide, "--data-dir", data}, "refused"); strings.Contains(o, "+old") {
		t.Fatalf("dry-run showed outside content: %q", o)
	}

	in := filepath.Join(ws, "f.txt")
	if err := os.WriteFile(in, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedAction(t, data, "S-none", ws, in, []byte("old"), []byte("new"), nil)
	if code, _, errOut := undoCLI(t, "--session", "S-none", "--data-dir", data); code != 1 || !strings.Contains(errOut, "no journaled sandbox") {
		t.Fatalf("unscoped action: exit %d stderr %q", code, errOut)
	}

	seedAction(t, data, "S-in", ws, in, []byte("old"), []byte("new"), []string{ws})
	denied := writeConfig(t, "[approval]\ndeny_paths = [\"**/f.txt\"]\n")
	wantUndo(t, 1, []string{"--session", "S-in", "--config", denied, "--data-dir", data})
	if got := readFile(t, in); string(got) != "new" {
		t.Fatalf("currently denied path restored: %q", got)
	}
	wantUndo(t, 0, []string{"--session", "S-in", "--data-dir", data}, " undone ")
	if got := readFile(t, in); string(got) != "old" {
		t.Fatalf("in-sandbox undo = %q, want old", got)
	}
}

// TestUndoActionIDScoped (REVIEW-F2-SEC5-2): a<id> must belong to the
// requested or directory-default session.
func TestUndoActionIDScoped(t *testing.T) {
	ws1, ws2, data := realDir(t), realDir(t), t.TempDir()
	a := filepath.Join(ws1, "a.txt")
	if err := os.WriteFile(a, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	idA := seedAction(t, data, "A", ws1, a, []byte("old"), []byte("new"), []string{ws1})
	seedAction(t, data, "B", ws2, filepath.Join(ws2, "b.txt"), []byte("old"), []byte("new"), []string{ws2})
	t.Chdir(ws2)
	for _, args := range [][]string{{aid(idA)}, {aid(idA), "--session", "B"}} {
		if code, _, errOut := undoCLI(t, append(args, "--data-dir", data)...); code != 1 || !strings.Contains(errOut, "is not in session") {
			t.Fatalf("undo %q: exit %d stderr %q", args, code, errOut)
		}
	}
	if got := readFile(t, a); string(got) != "new" {
		t.Fatalf("cross-session undo touched the file: %q", got)
	}
	wantUndo(t, 0, []string{aid(idA), "--session", "A", "--dry-run", "--data-dir", data}, "would restore the before image")
}

// TestUndoDryRunReadsThroughSandbox (REVIEW-F2-SEC5-3): a symlink to a
// file outside the roots, or any symlink, is refused without showing
// content.
func TestUndoDryRunReadsThroughSandbox(t *testing.T) {
	ws, other, data := realDir(t), realDir(t), t.TempDir()
	t.Chdir(ws)
	for name, target := range map[string]string{"out": filepath.Join(other, "secret"), "in": filepath.Join(ws, "real")} {
		if err := os.WriteFile(target, []byte("TOPSECRET"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(ws, name+".txt")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		seedAction(t, data, "S-"+name, ws, link, []byte("old"), []byte("TOPSECRET"), []string{ws})
		if o := wantUndo(t, 0, []string{"--dry-run", "--session", "S-" + name, "--data-dir", data}, "refused"); strings.Contains(o, "TOPSECRET") {
			t.Fatalf("%s symlink: dry-run leaked content: %q", name, o)
		}
	}
}

// TestUndoOutputSanitized (REVIEW-F2-SEC5-4): journaled paths and diff
// lines cannot emit ESC, bidi controls, or extra lines.
func TestUndoOutputSanitized(t *testing.T) {
	ws, data := realDir(t), t.TempDir()
	t.Chdir(ws)
	evil := ws + string(os.PathSeparator) + "a\x1b[31m\u202e\nFAKE b.txt"
	seedAction(t, data, "S", ws, evil, []byte("old\x1b[2J\u2066x\n"), []byte("new\n"), []string{ws})
	ls := wantUndo(t, 0, []string{"ls", "--data-dir", data})
	dry := wantUndo(t, 0, []string{"--dry-run", "--data-dir", data}, "conflict")
	for name, o := range map[string]string{"ls": ls, "dry-run": dry} {
		if strings.ContainsAny(o, "\x1b\u202e\u2066") {
			t.Fatalf("%s output not sanitized: %q", name, o)
		}
	}
	if strings.Count(ls, "\n") != 1 || !strings.Contains(ls, "a [31m FAKE b.txt") {
		t.Fatalf("ls = %q, want one sanitized line", ls)
	}
}
