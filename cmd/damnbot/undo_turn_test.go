package main

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// undoTwoTurns runs two turns on one session: beta -> gamma, then
// gamma -> delta. It returns the workspace, data dir and both turn ids.
func undoTwoTurns(t *testing.T) (ws, data, first, second string) {
	t.Helper()
	ws, data = undoWorkspace(t, []byte("alpha\nbeta\n"), 0o600)
	p := filepath.Join(ws, "f.txt")
	after := readFile(t, p)
	edit := map[string]any{
		"path": "f.txt", "tag": tagOf(after),
		"edits": []any{map[string]any{"old_string": "gamma", "new_string": "delta"}},
	}
	srv := providertest.New(t, nil,
		toolReply(t, "r2", "read_file", map[string]any{"path": "f.txt"}),
		toolReply(t, "c2", "edit_file", edit),
		providertest.Text("edited again"),
	)
	var out, errb bytes.Buffer
	code := run([]string{
		"run", "--continue", "-p", "edit again", "--model", "openai/gpt-fake", "--base-url", srv.URL,
		"--data-dir", data, "--output", "json", "--approval", "auto-edit",
		"--config", cwdTrustedRunConfig(t),
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("second run exit = %d, stderr = %s, stdout = %s", code, errb.String(), out.String())
	}
	if got := readFile(t, p); !bytes.Contains(got, []byte("delta")) {
		t.Fatalf("second edit not applied: %q", got)
	}
	db := openData(t, data)
	err := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		if gerr := tx.Get("SELECT turn_id FROM actions ORDER BY id ASC LIMIT 1", nil, &first); gerr != nil {
			return fmt.Errorf("first turn id: %w", gerr)
		}
		if gerr := tx.Get("SELECT turn_id FROM actions ORDER BY id DESC LIMIT 1", nil, &second); gerr != nil {
			return fmt.Errorf("last turn id: %w", gerr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read turn ids: %v", err)
	}
	if first == "" || second == "" || first == second {
		t.Fatalf("turn ids = (%q, %q), want two distinct ids", first, second)
	}
	return ws, data, first, second
}

// TestUndoTurnFilter: `--turn` undoes exactly the actions of that turn, newest
// first, and leaves the earlier turn's restore point intact.
func TestUndoTurnFilter(t *testing.T) {
	ws, data, first, second := undoTwoTurns(t)
	p := filepath.Join(ws, "f.txt")
	out := wantUndo(t, 0, []string{"--turn", second, "--dry-run", "--data-dir", data},
		"would restore the before image", "+gamma")
	if n := strings.Count(out, "would restore"); n != 1 {
		t.Fatalf("dry-run previewed %d actions, want 1:\n%s", n, out)
	}
	if got := readFile(t, p); !bytes.Contains(got, []byte("delta")) {
		t.Fatalf("dry-run wrote the file: %q", got)
	}
	if undone, _ := undoRows(t, data); undone != 0 {
		t.Fatalf("dry-run recorded %d undos", undone)
	}
	wantUndo(t, 0, []string{"--turn", second, "--data-dir", data}, " undone ")
	if got := readFile(t, p); !bytes.Equal(got, []byte("alpha\ngamma\n")) {
		t.Fatalf("after undoing turn 2 = %q, want the state left by turn 1", got)
	}
	wantUndo(t, 0, []string{"--turn", first, "--data-dir", data}, " undone ")
	if got := readFile(t, p); !bytes.Equal(got, []byte("alpha\nbeta\n")) {
		t.Fatalf("after undoing turn 1 = %q, want the original", got)
	}
	if undone, leases := undoRows(t, data); undone != 2 || leases != 0 {
		t.Fatalf("undone rows = %d, leases = %d", undone, leases)
	}
}

// TestUndoTurnUnknown: a turn with no journaled action is exit 1, never a
// silent success.
func TestUndoTurnUnknown(t *testing.T) {
	_, data, _, _ := undoTwoTurns(t)
	code, out, errOut := undoCLI(t, "--turn", "01JFAKE0000000000000000000", "--data-dir", data)
	if code != 1 || out != "" {
		t.Fatalf("unknown turn = (%d, %q)", code, out)
	}
	if !strings.Contains(errOut, "no journaled actions for turn") {
		t.Fatalf("stderr = %q", errOut)
	}
}

// TestUndoTurnSessionMismatch: an explicit --session that does not own the
// turn is refused, never undone in the wrong session.
func TestUndoTurnSessionMismatch(t *testing.T) {
	_, data, _, second := undoTwoTurns(t)
	code, out, errOut := undoCLI(t, "--turn", second, "--session", "01JFAKE0000000000000000001", "--data-dir", data)
	if code != 1 || out != "" {
		t.Fatalf("mismatched session = (%d, %q)", code, out)
	}
	if !strings.Contains(errOut, "not found") {
		t.Fatalf("stderr = %q", errOut)
	}
}

// TestUndoTurnParseConflicts: --turn is exclusive with N, a<id> and ls.
func TestUndoTurnParseConflicts(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"1", "a5", "ls"} {
		code, out, errOut := undoCLI(t, "--turn", "T1", target, "--data-dir", t.TempDir())
		if code != 2 || out != "" {
			t.Errorf("undo --turn T1 %s = (%d, %q), want exit 2", target, code, out)
		}
		if !strings.Contains(errOut, "no positional target") {
			t.Errorf("undo --turn T1 %s: stderr = %q", target, errOut)
		}
	}
	if code, _, _ := undoCLI(t, "--turn", "--data-dir", t.TempDir()); code != 2 {
		t.Errorf("--turn without a value: exit = %d, want 2", code)
	}
}
