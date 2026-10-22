package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/journalstore"
	"github.com/damnbot/damnbot/internal/journal"
)

// seedCheckpoints creates a closed data dir with one expired and one current
// journal action.
func seedCheckpoints(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: "S1", Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	record := func(now int64, path string) {
		js, jerr := journalstore.New(db, &journalstore.Options{Now: func() int64 { return now }})
		if jerr != nil {
			t.Fatal(jerr)
		}
		_, rerr := js.Record(ctx, &journal.Record{
			SessionID: "S1", Kind: journal.FileWrite, Path: path,
			Before: &journal.Snapshot{Data: []byte("x"), Mode: 0o600, ModeSet: true},
		})
		if rerr != nil {
			t.Fatalf("record %s: %v", path, rerr)
		}
	}
	record(time.Now().Add(-40*24*time.Hour).UnixMilli(), "/w/old")
	record(time.Now().UnixMilli(), "/w/new")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runCP(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = runCheckpoints(args, &out, &errb)
	return code, out.String(), errb.String()
}

// TestCheckpointsStatusList: the expired action is counted and listed.
func TestCheckpointsStatusList(t *testing.T) {
	t.Parallel()
	dir := seedCheckpoints(t)
	code, out, e := runCP(t, "status", "--data-dir", dir)
	if code != 0 || !strings.Contains(out, "actions: 2") || !strings.Contains(out, "expired: 1") {
		t.Fatalf("status = %d:\n%s\n%s", code, out, e)
	}
	code, out, e = runCP(t, "list", "--data-dir", dir)
	if code != 0 {
		t.Fatalf("list = %d: %s", code, e)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0]+lines[1], "expired") || !strings.Contains(lines[0]+lines[1], "ok") {
		t.Fatalf("list lines = %d:\n%s", len(lines), out)
	}
}

// TestCheckpointsPruneClear: dry-run previews, deletion needs --yes, and
// clear removes the rest.
func TestCheckpointsPruneClear(t *testing.T) {
	t.Parallel()
	dir := seedCheckpoints(t)
	if code, out, _ := runCP(t, "prune", "--dry-run", "--data-dir", dir); code != 0 || !strings.Contains(out, "would prune 1") {
		t.Fatalf("prune dry-run = %d %q", code, out)
	}
	if code, _, _ := runCP(t, "prune", "--data-dir", dir); code != 2 {
		t.Errorf("prune without --yes exit = %d, want 2", code)
	}
	if code, out, e := runCP(t, "prune", "--yes", "--data-dir", dir); code != 0 || !strings.Contains(out, "pruned 1") {
		t.Fatalf("prune = %d %q (%s)", code, out, e)
	}
	if code, out, _ := runCP(t, "status", "--data-dir", dir); code != 0 || !strings.Contains(out, "actions: 1") {
		t.Fatalf("status after prune = %d:\n%s", code, out)
	}
	if code, _, _ := runCP(t, "clear", "--data-dir", dir); code != 2 {
		t.Errorf("clear without --yes exit = %d, want 2", code)
	}
	if code, out, e := runCP(t, "clear", "--yes", "--data-dir", dir); code != 0 || !strings.Contains(out, "cleared 1") {
		t.Fatalf("clear = %d %q (%s)", code, out, e)
	}
	if code, out, _ := runCP(t, "status", "--data-dir", dir); code != 0 || !strings.Contains(out, "actions: 0") {
		t.Fatalf("status after clear = %d:\n%s", code, out)
	}
	if code, _, _ := runCP(t, "prune", "--older-than", "x", "--data-dir", dir); code != 2 {
		t.Errorf("bad --older-than exit = %d, want 2", code)
	}
}

// TestCheckpointsClearLegacy: the legacy directory is only removed with --yes.
func TestCheckpointsClearLegacy(t *testing.T) {
	t.Parallel()
	dir := seedCheckpoints(t)
	legacy := filepath.Join(dir, "checkpoints")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "old"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, e := runCP(t, "clear-legacy", "--data-dir", dir); code != 2 || !strings.Contains(e, "without --yes") {
		t.Fatalf("clear-legacy without --yes = %d %q", code, e)
	}
	if code, out, e := runCP(t, "clear-legacy", "--yes", "--data-dir", dir); code != 0 || !strings.Contains(out, "cleared legacy checkpoints") {
		t.Fatalf("clear-legacy = %d %q (%s)", code, out, e)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy checkpoint dir still exists: %v", err)
	}
	if code, out, _ := runCP(t, "clear-legacy", "--data-dir", dir); code != 0 || !strings.Contains(out, "no legacy checkpoints") {
		t.Fatalf("clear-legacy no-op = %d %q", code, out)
	}
}
