package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider/providertest"
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

// tagOf is the write_file/edit_file tag rule for small files: sha256[:4] hex,
// the same tag read_file hands the model.
func tagOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:4])
}

func toolCall(id, name, args string) providertest.Reply {
	return providertest.Reply{Frames: []string{
		fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":%q,"type":"function","function":{"name":%q,"arguments":%s}}]}}]}`,
			id, name, strconv.Quote(args)),
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	}}
}

func stopReply() providertest.Reply {
	return providertest.Reply{Frames: []string{
		`{"choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`,
		`[DONE]`,
	}}
}

// writeScript reads path (receiving its tag) and overwrites it.
func writeScript(path string, before, after []byte) []providertest.Reply {
	return []providertest.Reply{
		toolCall("c1-read", "read_file", fmt.Sprintf(`{"path":%q}`, path)),
		toolCall("c1-write", "write_file", fmt.Sprintf(`{"path":%q,"content":%q,"tag":%q}`, path, string(after), tagOf(before))),
	}
}

func editScript(path string, before []byte, old, new string) []providertest.Reply {
	return []providertest.Reply{
		toolCall("c2-read", "read_file", fmt.Sprintf(`{"path":%q}`, path)),
		toolCall("c2-edit", "edit_file", fmt.Sprintf(`{"path":%q,"tag":%q,"edits":[{"old_string":%q,"new_string":%q}]}`,
			path, tagOf(before), old, new)),
	}
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

// symlinkRoot puts the scenario's workspace behind a linked directory
// (macOS /var -> /private/var, Windows junction) and runs the child there.
func symlinkRoot(t *testing.T, e *Env) string {
	t.Helper()
	realWork := e.Workdir
	link := filepath.Join(t.TempDir(), "root-link")
	if err := dirLink(realWork, link); err != nil {
		skipOrFail(t, "directory link: "+err.Error())
	}
	e.Workdir = link
	return link
}

func isLink(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeSymlink != 0 || (runtime.GOOS == "windows" && fi.Mode()&os.ModeIrregular != 0)
}

func mustWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: fixture under t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestUndoSymlinkedRoot edits and writes two files through the real binary
// with the workspace reached through a linked root, then `damnbot undo 2`
// restores both byte-identically; `damnbot review` runs in the same store.
func TestUndoSymlinkedRoot(t *testing.T) {
	writeBefore, writeAfter := []byte("old-write\n"), []byte("new-write\n")
	editBefore := []byte("one\ntwo\n")
	replies := append(writeScript("write.txt", writeBefore, writeAfter),
		editScript("edit.txt", editBefore, "two", "TWO")...)
	replies = append(replies, stopReply())
	e := New(t, replies...)
	root := symlinkRoot(t, e)
	mustWrite(t, filepath.Join(root, "write.txt"), writeBefore)
	mustWrite(t, filepath.Join(root, "edit.txt"), editBefore)

	if res := e.Run("edit and write", "--approval", "auto"); res.Code != 0 {
		t.Fatalf("run = %d\nstdout: %s\nstderr: %s", res.Code, res.Stdout, res.Stderr)
	}
	if got := mustRead(t, filepath.Join(root, "write.txt")); !bytes.Equal(got, writeAfter) {
		t.Fatalf("after run write = %q", got)
	}
	if got := mustRead(t, filepath.Join(root, "edit.txt")); !bytes.Equal(got, []byte("one\nTWO\n")) {
		t.Fatalf("after run edit = %q", got)
	}

	if res := e.Exec("undo", "2", "--data-dir", e.DataDir); res.Code != 0 {
		t.Fatalf("undo = %d\nstdout: %s\nstderr: %s", res.Code, res.Stdout, res.Stderr)
	}
	if got := mustRead(t, filepath.Join(root, "write.txt")); !bytes.Equal(got, writeBefore) {
		t.Fatalf("after undo write = %q, want %q", got, writeBefore)
	}
	if got := mustRead(t, filepath.Join(root, "edit.txt")); !bytes.Equal(got, editBefore) {
		t.Fatalf("after undo edit = %q, want %q", got, editBefore)
	}
	if res := e.Exec("review", "--data-dir", e.DataDir); res.Code != 0 {
		t.Fatalf("review = %d\nstdout: %s\nstderr: %s", res.Code, res.Stdout, res.Stderr)
	}
}

// TestUndoSymlinkInsideRefused makes the journaled path's directory a link
// whose target carries the same after-image: only the no-symlink rule can
// refuse. undo (and undo --force) must refuse, leaving target and link intact.
func TestUndoSymlinkInsideRefused(t *testing.T) {
	before, after := []byte("before\n"), []byte("after\n")
	e := New(t, append(writeScript("dir/note.txt", before, after), stopReply())...)
	root := symlinkRoot(t, e)
	dir := filepath.Join(root, "dir")
	note := filepath.Join(dir, "note.txt")
	mustWrite(t, note, before)
	if res := e.Run("write the note", "--approval", "auto"); res.Code != 0 {
		t.Fatalf("run = %d\nstdout: %s\nstderr: %s", res.Code, res.Stdout, res.Stderr)
	}
	if got := mustRead(t, note); !bytes.Equal(got, after) {
		t.Fatalf("after run = %q", got)
	}
	// Same bytes behind a new directory; the journaled path now crosses a
	// link, so only the symlink rule can refuse.
	elsewhere := filepath.Join(root, "dir-elsewhere")
	mustWrite(t, filepath.Join(elsewhere, "note.txt"), after)
	if err := os.Rename(dir, filepath.Join(root, "dir-orig")); err != nil {
		t.Fatal(err)
	}
	if err := dirLink(elsewhere, dir); err != nil {
		t.Fatalf("relink dir: %v", err)
	}
	for _, args := range [][]string{
		{"undo", "--data-dir", e.DataDir},
		{"undo", "--force", "--data-dir", e.DataDir},
	} {
		res := e.Exec(args...)
		if res.Code == 0 {
			t.Fatalf("%v accepted a linked directory below the root\nstdout: %s", args, res.Stdout)
		}
		if !strings.Contains(res.Stderr, "is now a symlink") {
			t.Fatalf("%v stderr = %q, want the symlink refusal", args, res.Stderr)
		}
		if got := mustRead(t, filepath.Join(elsewhere, "note.txt")); !bytes.Equal(got, after) {
			t.Fatalf("%v changed the link target: %q", args, got)
		}
		if !isLink(dir) {
			t.Fatalf("%v replaced the link", args)
		}
	}
}

// TestUndoWithoutSymlinkSucceeds is the control for the test above: the same
// flow without the directory swap undoes and restores the before image.
func TestUndoWithoutSymlinkSucceeds(t *testing.T) {
	before, after := []byte("before\n"), []byte("after\n")
	e := New(t, append(writeScript("dir/note.txt", before, after), stopReply())...)
	note := filepath.Join(e.Workdir, "dir", "note.txt")
	mustWrite(t, note, before)
	if res := e.Run("write the note", "--approval", "auto"); res.Code != 0 {
		t.Fatalf("run = %d\nstdout: %s\nstderr: %s", res.Code, res.Stdout, res.Stderr)
	}
	if res := e.Exec("undo", "--data-dir", e.DataDir); res.Code != 0 {
		t.Fatalf("undo = %d\nstdout: %s\nstderr: %s", res.Code, res.Stdout, res.Stderr)
	}
	if got := mustRead(t, note); !bytes.Equal(got, before) {
		t.Fatalf("after undo = %q, want %q", got, before)
	}
}

// TestUndoRefusalContentConflict changes the file after the action: undo must
// refuse with the content-conflict path and leave the external bytes, while
// --force restores the before image.
func TestUndoRefusalContentConflict(t *testing.T) {
	before, after := []byte("before\n"), []byte("after\n")
	e := New(t, append(writeScript("note.txt", before, after), stopReply())...)
	note := filepath.Join(e.Workdir, "note.txt")
	mustWrite(t, note, before)
	if res := e.Run("write the note", "--approval", "auto"); res.Code != 0 {
		t.Fatalf("run = %d\nstdout: %s\nstderr: %s", res.Code, res.Stdout, res.Stderr)
	}
	mustWrite(t, note, []byte("other\n"))
	res := e.Exec("undo", "--data-dir", e.DataDir)
	if res.Code == 0 || !strings.Contains(res.Stderr, "conflict") {
		t.Fatalf("undo = %d, stderr %q; want a conflict refusal", res.Code, res.Stderr)
	}
	if got := mustRead(t, note); !bytes.Equal(got, []byte("other\n")) {
		t.Fatalf("refused undo changed the file: %q", got)
	}
	if res := e.Exec("undo", "--force", "--data-dir", e.DataDir); res.Code != 0 {
		t.Fatalf("forced undo = %d\nstdout: %s\nstderr: %s", res.Code, res.Stdout, res.Stderr)
	}
	if got := mustRead(t, note); !bytes.Equal(got, before) {
		t.Fatalf("forced undo = %q, want %q", got, before)
	}
}
