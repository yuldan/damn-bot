package e2e

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/journalstore"
	"github.com/damnbot/damnbot/internal/journal"
)

// TestCheckpointsCLI: status/list/prune/clear operate on the journal action
// store through the child binary (CLI-039).
func TestCheckpointsCLI(t *testing.T) {
	t.Parallel()
	e := New(t)
	ctx := t.Context()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: e.DataDir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if _, cErr := db.CreateSession(ctx, ctxdb.Session{ID: "cp", Origin: "headless", Cwd: "/w"}); cErr != nil {
		t.Fatal(cErr)
	}
	js, err := journalstore.New(db, &journalstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, recErr := js.Record(ctx, &journal.Record{
		SessionID: "cp", Kind: journal.FileWrite, Path: "/w/checkpoint.txt",
		Before: &journal.Snapshot{Data: []byte("v1"), Mode: 0o600, ModeSet: true},
	}); recErr != nil {
		t.Fatal(recErr)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

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
	if legacy := e.Exec("checkpoints", "clear-legacy", "--data-dir", e.DataDir); !strings.Contains(legacy.Stdout, "no legacy checkpoints") {
		t.Errorf("clear-legacy = %q", legacy.Stdout)
	}
}
