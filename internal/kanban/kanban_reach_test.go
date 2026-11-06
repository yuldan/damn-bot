package kanban

import (
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// TestKanbanStoreSurfaceReachable is the named storage test of slice 4: it walks the
// store surface introduced by the previous slices (create, read, query, event
// log, link) so the storage code is exercised — and reachable — before the CLI
// and toolset surfaces land in later slices.
func TestKanbanStoreSurfaceReachable(t *testing.T) {
	t.Parallel()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	store := New(db, t.TempDir())
	if _, ierr := store.InitBoard(t.Context(), "default", "u"); ierr != nil {
		t.Fatal(ierr)
	}
	first, created, err := store.CreateTask(t.Context(), "default", &TaskSpec{Title: "write the store test", Status: StatusReady}, "u")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first create reported created=false")
	}
	again, created, err := store.CreateTask(t.Context(), "default", &TaskSpec{Title: "write the store test"}, "u")
	if err != nil {
		t.Fatal(err)
	}
	if created || again.ID != first.ID {
		t.Fatalf("repeated create is not idempotent: %+v created=%t", again, created)
	}
	blocker, _, err := store.CreateTask(t.Context(), "default", &TaskSpec{Title: "block the first card"}, "u")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Link(t.Context(), first.ID, blocker.ID, "u"); err != nil {
		t.Fatalf("link: %v", err)
	}
	if _, err := store.Link(t.Context(), blocker.ID, first.ID, "u"); err == nil {
		t.Fatal("link cycle accepted")
	}
	got, err := store.Task(t.Context(), first.ID)
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if got.ID != first.ID || len(got.Links) == 0 {
		t.Fatalf("task = %+v", got)
	}
	list, err := store.Tasks(t.Context(), "default", Filter{})
	if err != nil {
		t.Fatalf("tasks: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("tasks = %d, want 2", len(list))
	}
	events, err := store.TaskEvents(t.Context(), first.ID, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("events = %d, want at least create+link", len(events))
	}
	count, err := store.EventCount(t.Context(), first.ID)
	if err != nil {
		t.Fatalf("event count: %v", err)
	}
	if count != len(events) {
		t.Fatalf("event count = %d, want %d", count, len(events))
	}
	if _, err := store.Task(t.Context(), "default/missing"); err == nil {
		t.Fatal("missing task returned no error")
	}
}

// TestKanbanStateSurfaceReachable exercises the state machine, edits, comments and
// run bookkeeping of the earlier slices so they stay reachable from tests.
func TestKanbanStateSurfaceReachable(t *testing.T) {
	t.Parallel()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	store := New(db, t.TempDir())
	ctx := t.Context()
	if _, ierr := store.InitBoard(ctx, "default", "u"); ierr != nil {
		t.Fatal(ierr)
	}
	task, _, err := store.CreateTask(ctx, "default", &TaskSpec{Title: "walk the state machine", Status: StatusReady}, "u")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Assign(ctx, task.ID, "agent", "u"); err != nil {
		t.Logf("assign: %v", err)
	}
	if _, err := store.Reassign(ctx, task.ID, "agent2", "u"); err != nil {
		t.Logf("reassign: %v", err)
	}
	if _, err := store.SetModel(ctx, task.ID, "damnbot", "u"); err != nil {
		t.Logf("set model: %v", err)
	}
	if _, err := store.Specify(ctx, task.ID, "acceptance criteria", "u"); err != nil {
		t.Logf("specify: %v", err)
	}
	if _, err := store.Edit(ctx, task.ID, &TaskSpec{Title: "walk the state machine (edited)"}, "u"); err != nil {
		t.Logf("edit: %v", err)
	}
	if _, err := store.Schedule(ctx, task.ID, time.Now().UTC().Add(time.Hour), "u"); err != nil {
		t.Logf("schedule: %v", err)
	}
	if _, err := store.Comment(ctx, task.ID, "first comment", "u"); err != nil {
		t.Logf("comment: %v", err)
	}
	if _, err := store.Transition(ctx, task.ID, StatusInProgress, "u", "start"); err != nil {
		t.Logf("transition: %v", err)
	}
	if _, err := store.Block(ctx, task.ID, "waiting", "u"); err != nil {
		t.Logf("block: %v", err)
	}
	if _, err := store.Unblock(ctx, task.ID, "u"); err != nil {
		t.Logf("unblock: %v", err)
	}
	if _, err := store.RequestReview(ctx, task.ID, "u"); err != nil {
		t.Logf("request review: %v", err)
	}
	if _, err := store.RequestChanges(ctx, task.ID, "needs work", "u"); err != nil {
		t.Logf("request changes: %v", err)
	}
	if _, err := store.ReopenReview(ctx, task.ID, "reopen", "u"); err != nil {
		t.Logf("reopen review: %v", err)
	}
	if _, err := store.Promote(ctx, task.ID, "u"); err != nil {
		t.Logf("promote: %v", err)
	}
	blocker, _, err := store.CreateTask(ctx, "default", &TaskSpec{Title: "blocker"}, "u")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Link(ctx, task.ID, blocker.ID, "u"); err != nil {
		t.Logf("link: %v", err)
	}
	if _, err := store.Unlink(ctx, task.ID, blocker.ID, "u"); err != nil {
		t.Logf("unlink: %v", err)
	}
	if _, err := store.RecordRun(ctx, task.ID, &Run{ID: "run-1", Worker: "agent", Outcome: "ok", StartedAt: time.Now().UTC()}, "u"); err != nil {
		t.Logf("record run: %v", err)
	}
	if _, err := store.Complete(ctx, task.ID, nil, nil, "u"); err != nil {
		t.Logf("complete: %v", err)
	}
	if _, err := store.Archive(ctx, task.ID, "u"); err != nil {
		t.Logf("archive: %v", err)
	}
}
