package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/tui"
)

func testActions(t *testing.T, db *ctxdb.Store) *tuiActions {
	t.Helper()
	cfg := config.Default()
	return &tuiActions{
		db:  func(context.Context) (*ctxdb.Store, error) { return db, nil },
		cfg: &cfg, configDir: t.TempDir(), modelRef: "fake/model",
	}
}

func mustNew(ctx context.Context, t *testing.T, a *tuiActions, db *ctxdb.Store) tui.SessionChange {
	t.Helper()
	change, err := a.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if change.SessionID == "" || !strings.Contains(change.Detail, change.SessionID) {
		t.Fatalf("New = %+v", change)
	}
	if _, _, merr := sessionMeta(ctx, db, change.SessionID); merr != nil {
		t.Fatalf("new session row: %v", merr)
	}
	return change
}

// checkLink asserts parent_id/fork_seq of a branch child.
func checkLink(t *testing.T, db *ctxdb.Store, child, parent string, want int64) {
	t.Helper()
	var pid string
	var seq int64
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT parent_id, fork_seq FROM sessions WHERE id = ?1`, []any{child}, &pid, &seq)
	}); err != nil {
		t.Fatalf("branch row: %v", err)
	}
	if pid != parent || seq != want {
		t.Fatalf("child parent/fork = %q/%d, want %q/%d", pid, seq, parent, want)
	}
}

// TestTUIActionsNew creates a real session row in a tempdir store.
func TestTUIActionsNew(t *testing.T) {
	t.Chdir(realDir(t))
	db := openData(t, t.TempDir())
	mustNew(t.Context(), t, testActions(t, db), db)
}

// TestTUIActionsForkRewind exercises the real ctxdb branch primitives: fork at
// head, rewind at an earlier seq, and refusal of a rewind at head or a bad --at.
func TestTUIActionsForkRewind(t *testing.T) {
	t.Chdir(realDir(t))
	db := openData(t, t.TempDir())
	a := testActions(t, db)
	ctx := t.Context()
	parent := mustNew(ctx, t, a, db)

	if _, err := db.Append(ctx, parent.SessionID, nil, ctxdb.Event{
		Kind: "user_message", Role: "user", Content: "hi", TurnID: "t1",
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if head, err := db.Head(ctx, parent.SessionID); err != nil || head != 1 {
		t.Fatalf("head = %d, %v, want 1", head, err)
	}

	child, err := a.Fork(ctx, parent.SessionID)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if child.SessionID == "" || child.SessionID == parent.SessionID {
		t.Fatalf("Fork returned parent id: %+v", child)
	}
	checkLink(t, db, child.SessionID, parent.SessionID, 1)

	back, err := a.Rewind(ctx, parent.SessionID, "0")
	if err != nil {
		t.Fatalf("Rewind(0): %v", err)
	}
	if back.SessionID == "" || back.SessionID == parent.SessionID {
		t.Fatalf("Rewind returned parent id: %+v", back)
	}
	checkLink(t, db, back.SessionID, parent.SessionID, 0)

	for _, at := range []string{"1", "bogus", "turn:missing"} {
		if _, rerr := a.Rewind(ctx, parent.SessionID, at); rerr == nil {
			t.Fatalf("rewind %q was accepted", at)
		}
	}
}

// TestTUIActionsUndoRealStore restores a real journaled file in a tempdir
// through the same undoApply path as `damnbot undo`.
func TestTUIActionsUndoRealStore(t *testing.T) {
	ws, data := realDir(t), t.TempDir()
	t.Chdir(ws)
	target := filepath.Join(ws, "note.txt")
	if err := os.WriteFile(target, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := seedAction(t, data, "S-a4", ws, target, []byte("old"), []byte("new"), []string{ws})
	a := testActions(t, openData(t, data))

	detail, err := a.Undo(t.Context(), "S-a4", fmt.Sprintf("a%d", id))
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if !strings.Contains(detail, "undone") {
		t.Fatalf("undo detail = %q, want undone", detail)
	}
	if got := string(readFile(t, target)); got != "old" {
		t.Fatalf("file = %q, want the before image %q", got, "old")
	}
	if _, err := a.Undo(t.Context(), "S-a4", "a999999"); err == nil {
		t.Fatalf("unknown action reported success")
	}
}

// TestTUIActionsUndoRefusals: an active turn and an invalid target are errors,
// and the file is not touched by a refusal.
func TestTUIActionsUndoRefusals(t *testing.T) {
	ws, data := realDir(t), t.TempDir()
	t.Chdir(ws)
	target := filepath.Join(ws, "note.txt")
	if err := os.WriteFile(target, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := seedAction(t, data, "S-busy", ws, target, []byte("old"), []byte("new"), []string{ws})
	a := testActions(t, openData(t, data))
	a.active = func(string) bool { return true }
	if _, err := a.Undo(t.Context(), "S-busy", fmt.Sprintf("a%d", id)); err == nil ||
		!strings.Contains(err.Error(), "turn is running") {
		t.Fatalf("active-turn undo = %v, want a refusal", err)
	}
	if got := string(readFile(t, target)); got != "new" {
		t.Fatalf("refused undo touched the file: %q", got)
	}
	a.active = nil
	if _, err := a.Undo(t.Context(), "S-busy", "not-an-action"); err == nil {
		t.Fatalf("invalid action target reported success")
	}
	if _, err := a.Undo(t.Context(), "S-busy", ""); err != nil {
		t.Fatalf("undo last action: %v", err)
	}
	if got := string(readFile(t, target)); got != "old" {
		t.Fatalf("undo last did not restore: %q", got)
	}
}
