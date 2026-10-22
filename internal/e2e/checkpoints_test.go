package e2e

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/journalstore"
	"github.com/damnbot/damnbot/internal/journal"
)

// seedCheckpoint records one journal action in the scenario's data dir and
// releases the session lease (a finished run does the same).
func seedCheckpoint(t *testing.T, e *Env, sid, path string) {
	t.Helper()
	ctx := t.Context()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: e.DataDir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if _, cErr := db.CreateSession(ctx, ctxdb.Session{ID: sid, Origin: "headless", Cwd: "/w"}); cErr != nil {
		t.Fatal(cErr)
	}
	js, err := journalstore.New(db, &journalstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.Record(ctx, &journal.Record{
		SessionID: sid, Kind: journal.FileWrite, Path: path,
		Before: &journal.Snapshot{Data: []byte("v1"), Mode: 0o600, ModeSet: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.ReleaseLease(ctx, sid); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestCheckpointsCLI: status/list and the destructive-path negative controls
// (no --yes deletes nothing, the default window keeps a fresh action, clear
// --yes removes it) through the child binary (CLI-039, SEC15-1..4).
func TestCheckpointsCLI(t *testing.T) {
	t.Parallel()
	e := New(t)
	seedCheckpoint(t, e, "cp", "/w/checkpoint.txt")

	status := e.Exec("checkpoints", "status", "--data-dir", e.DataDir)
	status.RequireOK(t)
	if !strings.Contains(status.Stdout, "actions: 1") || !strings.Contains(status.Stdout, "bytes: ") {
		t.Errorf("status:\n%s", status.Stdout)
	}
	list := e.Exec("checkpoints", "list", "--data-dir", e.DataDir)
	list.RequireOK(t)
	if !strings.Contains(list.Stdout, "file_write") || !strings.Contains(list.Stdout, "checkpoint.txt") {
		t.Errorf("list:\n%s", list.Stdout)
	}
	// The default 30-day window keeps a fresh action.
	fresh := e.Exec("checkpoints", "prune", "--dry-run", "--data-dir", e.DataDir)
	fresh.RequireOK(t)
	if !strings.Contains(fresh.Stdout, "would prune 0") {
		t.Errorf("default dry run = %q", fresh.Stdout)
	}
	// Without --yes nothing is deleted.
	noYes := e.Exec("checkpoints", "prune", "--older-than", "1ns", "--data-dir", e.DataDir)
	if noYes.Code != 2 {
		t.Fatalf("prune without --yes exit = %d, want 2\nstderr:\n%s", noYes.Code, noYes.Stderr)
	}
	still := e.Exec("checkpoints", "status", "--data-dir", e.DataDir)
	still.RequireOK(t)
	if !strings.Contains(still.Stdout, "actions: 1") {
		t.Errorf("prune without --yes deleted actions:\n%s", still.Stdout)
	}
	dry := e.Exec("checkpoints", "prune", "--older-than", "1ns", "--dry-run", "--data-dir", e.DataDir)
	dry.RequireOK(t)
	if !strings.Contains(dry.Stdout, "would prune 1") {
		t.Errorf("dry run = %q", dry.Stdout)
	}
	prune := e.Exec("checkpoints", "prune", "--older-than", "1ns", "--yes", "--data-dir", e.DataDir)
	prune.RequireOK(t)
	if !strings.Contains(prune.Stdout, "pruned 1") {
		t.Errorf("prune = %q", prune.Stdout)
	}
	after := e.Exec("checkpoints", "status", "--data-dir", e.DataDir)
	after.RequireOK(t)
	if !strings.Contains(after.Stdout, "actions: 0") {
		t.Errorf("status after prune:\n%s", after.Stdout)
	}
	// A second action proves clear --yes removes it.
	seedCheckpoint(t, e, "cp2", "/w/second.txt")
	seedAgain := e.Exec("checkpoints", "status", "--data-dir", e.DataDir)
	seedAgain.RequireOK(t)
	if !strings.Contains(seedAgain.Stdout, "actions: 1") {
		t.Fatalf("second seed:\n%s", seedAgain.Stdout)
	}
	clear := e.Exec("checkpoints", "clear", "--yes", "--data-dir", e.DataDir)
	clear.RequireOK(t)
	if !strings.Contains(clear.Stdout, "cleared 1") {
		t.Errorf("clear = %q", clear.Stdout)
	}
	empty := e.Exec("checkpoints", "status", "--data-dir", e.DataDir)
	empty.RequireOK(t)
	if !strings.Contains(empty.Stdout, "actions: 0") {
		t.Errorf("status after clear:\n%s", empty.Stdout)
	}
	if legacy := e.Exec("checkpoints", "clear-legacy", "--data-dir", e.DataDir); !strings.Contains(legacy.Stdout, "no legacy checkpoints") {
		t.Errorf("clear-legacy = %q", legacy.Stdout)
	}
}
