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

// holdTurn opens a lease and a turn journal row for sid and returns the
// function that closes both: a live lease or an open turn is what checkpoint
// maintenance must refuse (SEC15-1/SEC15-9).
func holdTurn(t *testing.T, e *Env, sid string) func() {
	t.Helper()
	ctx := t.Context()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: e.DataDir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	fail := func(step string, err error) {
		t.Helper()
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
		t.Fatalf("%s: %v", step, err)
	}
	if _, err := db.AcquireLease(ctx, sid); err != nil {
		fail("acquire lease", err)
	}
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error { return tx.StartTurn(sid, "e2e-open-turn") }); err != nil {
		fail("start turn", err)
	}
	return func() {
		if err := db.Write(ctx, func(tx *ctxdb.Tx) error { return tx.EndTurn(sid, "e2e-open-turn", "done") }); err != nil {
			t.Errorf("end turn: %v", err)
		}
		if err := db.ReleaseLease(ctx, sid); err != nil {
			t.Errorf("release lease: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}
}

// TestCheckpointsCLIDryRunScopeBusy drives --dry-run, --session and the busy
// refusal through the child binary (ACC-62, ACC-63, SEC15-9). Every security
// assertion carries a negative control: the same command after the guard is
// lifted must succeed, and the previews must leave the rows in place.
func TestCheckpointsCLIDryRunScopeBusy(t *testing.T) {
	t.Parallel()
	e := New(t)
	seedCheckpoint(t, e, "cp", "/w/checkpoint.txt")
	seedCheckpoint(t, e, "other", "/w/other.txt")

	// ACC-63: --dry-run previews, with and without --yes, and deletes nothing.
	for _, extra := range [][]string{{}, {"--yes"}} {
		args := append([]string{"checkpoints", "clear", "--dry-run"}, append(extra, "--data-dir", e.DataDir)...)
		dry := e.Exec(args...)
		dry.RequireOK(t)
		if !strings.Contains(dry.Stdout, "would clear 2") {
			t.Errorf("clear --dry-run %v = %q", extra, dry.Stdout)
		}
		kept := e.Exec("checkpoints", "status", "--data-dir", e.DataDir)
		kept.RequireOK(t)
		if !strings.Contains(kept.Stdout, "actions: 2") {
			t.Fatalf("clear --dry-run %v deleted actions:\n%s", extra, kept.Stdout)
		}
	}
	if legacy := e.Exec("checkpoints", "clear-legacy", "--dry-run", "--yes", "--data-dir", e.DataDir); !strings.Contains(legacy.Stdout, "no legacy checkpoints") {
		t.Errorf("clear-legacy --dry-run = %q", legacy.Stdout)
	}

	// ACC-62: --session scopes status and list; unscoped sees both sessions.
	scoped := e.Exec("checkpoints", "status", "--session", "cp", "--data-dir", e.DataDir)
	scoped.RequireOK(t)
	if !strings.Contains(scoped.Stdout, "actions: 1") {
		t.Errorf("status --session cp = %q", scoped.Stdout)
	}
	all := e.Exec("checkpoints", "status", "--data-dir", e.DataDir)
	all.RequireOK(t)
	if !strings.Contains(all.Stdout, "actions: 2") {
		t.Errorf("unscoped status = %q", all.Stdout)
	}
	hit := e.Exec("checkpoints", "list", "--session", "cp", "--data-dir", e.DataDir)
	hit.RequireOK(t)
	if !strings.Contains(hit.Stdout, "checkpoint.txt") {
		t.Errorf("list --session cp = %q", hit.Stdout)
	}
	miss := e.Exec("checkpoints", "list", "--session", "other", "--data-dir", e.DataDir)
	miss.RequireOK(t)
	if strings.Contains(miss.Stdout, "checkpoint.txt") {
		t.Errorf("list --session other leaked cp: %q", miss.Stdout)
	}
	if unknown := e.Exec("checkpoints", "status", "--session", "nope", "--data-dir", e.DataDir); unknown.Code == 0 {
		t.Errorf("status --session nope exit = 0, want non-zero")
	}
	// A filter the destructive subcommands do not implement is refused, not ignored.
	refused := e.Exec("checkpoints", "clear", "--session", "cp", "--yes", "--data-dir", e.DataDir)
	if refused.Code != 2 || !strings.Contains(refused.Stderr, "not supported") {
		t.Fatalf("clear --session = %d %q", refused.Code, refused.Stderr)
	}
	if pruned := e.Exec("checkpoints", "prune", "--session", "cp", "--older-than", "1ns", "--yes", "--data-dir", e.DataDir); pruned.Code != 2 {
		t.Fatalf("prune --session exit = %d, want 2", pruned.Code)
	}
	intact := e.Exec("checkpoints", "status", "--data-dir", e.DataDir)
	intact.RequireOK(t)
	if !strings.Contains(intact.Stdout, "actions: 2") {
		t.Fatalf("refused subcommands changed state:\n%s", intact.Stdout)
	}

	// SEC15-9: an open turn blocks the delete (exit 4) and keeps every row.
	endTurn := holdTurn(t, e, "cp")
	busy := e.Exec("checkpoints", "clear", "--yes", "--data-dir", e.DataDir)
	if busy.Code != 4 {
		t.Fatalf("clear with an open turn = %d (stderr %q), want 4", busy.Code, busy.Stderr)
	}
	stillThere := e.Exec("checkpoints", "status", "--data-dir", e.DataDir)
	stillThere.RequireOK(t)
	if !strings.Contains(stillThere.Stdout, "actions: 2") {
		t.Fatalf("busy refusal deleted actions:\n%s", stillThere.Stdout)
	}
	// The guard does not swallow the maintenance guard: an env marker (what the
	// shell tool sets, internal/exec.MarkerEnv) refuses with exit 2 as well.
	guard := e.ExecEnv([]string{"DAMNBOT_EXEC_ID=e2e"}, "checkpoints", "clear", "--yes", "--data-dir", e.DataDir)
	if guard.Code != 2 {
		t.Fatalf("clear under the shell marker = %d (stderr %q), want 2", guard.Code, guard.Stderr)
	}
	if marked := e.Exec("checkpoints", "status", "--data-dir", e.DataDir); !strings.Contains(marked.Stdout, "actions: 2") {
		t.Fatalf("marker refusal deleted actions:\n%s", marked.Stdout)
	}
	endTurn()

	// Negative control: with the turn closed the same command clears both rows.
	cleared := e.Exec("checkpoints", "clear", "--yes", "--data-dir", e.DataDir)
	cleared.RequireOK(t)
	if !strings.Contains(cleared.Stdout, "cleared 2") {
		t.Errorf("clear after the turn closed = %q", cleared.Stdout)
	}
	empty := e.Exec("checkpoints", "status", "--data-dir", e.DataDir)
	empty.RequireOK(t)
	if !strings.Contains(empty.Stdout, "actions: 0") {
		t.Errorf("status after clear:\n%s", empty.Stdout)
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
