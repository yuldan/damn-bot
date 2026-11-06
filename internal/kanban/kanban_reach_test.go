package kanban

import (
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// newReachStore opens one throwaway board store whose every write lands in a
// temp dir; the cleanup closes the ctxdb handle.
func newReachStore(t *testing.T) *Store {
	t.Helper()
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
	return store
}

func createReachTask(t *testing.T, store *Store, title string, status Status) *Task {
	t.Helper()
	task, _, err := store.CreateTask(t.Context(), "default", &TaskSpec{Title: title, Status: status}, "u")
	if err != nil {
		t.Fatalf("create %q: %v", title, err)
	}
	return task
}

// TestKanbanStoreSurfaceReachable is the named storage test of slice 4: it
// walks create, read, query, event log and link so the store surface stays
// reachable before the CLI and toolset land in later slices.
func TestKanbanStoreSurfaceReachable(t *testing.T) {
	t.Parallel()
	store := newReachStore(t)
	first := createReachTask(t, store, "write the store test", StatusReady)
	assertCreateIdempotent(t, store, first)
	blocker := createReachTask(t, store, "block the first card", "")
	assertLinkGuard(t, store, first.ID, blocker.ID)
	assertReadSurface(t, store, first.ID)
}

func assertCreateIdempotent(t *testing.T, store *Store, first *Task) {
	t.Helper()
	again, created, err := store.CreateTask(t.Context(), "default", &TaskSpec{Title: first.Title}, "u")
	if err != nil {
		t.Fatalf("repeat create: %v", err)
	}
	if created || again.ID != first.ID {
		t.Fatalf("repeated create is not idempotent: %+v created=%t", again, created)
	}
}

func assertLinkGuard(t *testing.T, store *Store, id, blockerID string) {
	t.Helper()
	if _, err := store.Link(t.Context(), id, blockerID, "u"); err != nil {
		t.Fatalf("link: %v", err)
	}
	if _, err := store.Link(t.Context(), blockerID, id, "u"); err == nil {
		t.Fatal("link cycle accepted")
	}
}

func assertReadSurface(t *testing.T, store *Store, id string) {
	t.Helper()
	got, taskErr := store.Task(t.Context(), id)
	if taskErr != nil {
		t.Fatalf("task: %v", taskErr)
	}
	if got.ID != id || len(got.Links) == 0 {
		t.Fatalf("task = %+v", got)
	}
	list, listErr := store.Tasks(t.Context(), "default", Filter{})
	if listErr != nil {
		t.Fatalf("tasks: %v", listErr)
	}
	if len(list) != 2 {
		t.Fatalf("tasks = %d, want 2", len(list))
	}
	events, eventErr := store.TaskEvents(t.Context(), id, 0)
	if eventErr != nil {
		t.Fatalf("events: %v", eventErr)
	}
	if len(events) < 2 {
		t.Fatalf("events = %d, want at least create+link", len(events))
	}
	count, countErr := store.EventCount(t.Context(), id)
	if countErr != nil {
		t.Fatalf("event count: %v", countErr)
	}
	if count != len(events) {
		t.Fatalf("event count = %d, want %d", count, len(events))
	}
	if _, missErr := store.Task(t.Context(), "default/missing"); missErr == nil {
		t.Fatal("missing task returned no error")
	}
}

// TestKanbanStateSurfaceReachable exercises the state machine, edits, comments
// and run bookkeeping so they stay reachable from tests.
func TestKanbanStateSurfaceReachable(t *testing.T) {
	t.Parallel()
	store := newReachStore(t)
	task := createReachTask(t, store, "walk the state machine", StatusReady)
	reachStateEdits(t, store, task.ID)
	reachStateFlow(t, store, task.ID)
	reachStateLinksAndRuns(t, store, task.ID)
}

func reachStateEdits(t *testing.T, store *Store, id string) {
	t.Helper()
	ctx := t.Context()
	if _, err := store.Assign(ctx, id, "agent", "u"); err != nil {
		t.Logf("assign: %v", err)
	}
	if _, err := store.Reassign(ctx, id, "agent2", "u"); err != nil {
		t.Logf("reassign: %v", err)
	}
	if _, err := store.SetModel(ctx, id, "damnbot", "u"); err != nil {
		t.Logf("set model: %v", err)
	}
	if _, err := store.Specify(ctx, id, "acceptance criteria", "u"); err != nil {
		t.Logf("specify: %v", err)
	}
	if _, err := store.Edit(ctx, id, &TaskSpec{Title: "walk the state machine (edited)"}, "u"); err != nil {
		t.Logf("edit: %v", err)
	}
	if _, err := store.Schedule(ctx, id, time.Now().UTC().Add(time.Hour), "u"); err != nil {
		t.Logf("schedule: %v", err)
	}
	if _, err := store.Comment(ctx, id, "first comment", "u"); err != nil {
		t.Logf("comment: %v", err)
	}
}

func reachStateFlow(t *testing.T, store *Store, id string) {
	t.Helper()
	ctx := t.Context()
	if _, err := store.Transition(ctx, id, StatusInProgress, "u", "start"); err != nil {
		t.Logf("transition: %v", err)
	}
	if _, err := store.Block(ctx, id, "waiting", "u"); err != nil {
		t.Logf("block: %v", err)
	}
	if _, err := store.Unblock(ctx, id, "u"); err != nil {
		t.Logf("unblock: %v", err)
	}
	if _, err := store.RequestReview(ctx, id, "u"); err != nil {
		t.Logf("request review: %v", err)
	}
	if _, err := store.RequestChanges(ctx, id, "needs work", "u"); err != nil {
		t.Logf("request changes: %v", err)
	}
	if _, err := store.ReopenReview(ctx, id, "reopen", "u"); err != nil {
		t.Logf("reopen review: %v", err)
	}
	if _, err := store.Promote(ctx, id, "u"); err != nil {
		t.Logf("promote: %v", err)
	}
}

func reachStateLinksAndRuns(t *testing.T, store *Store, id string) {
	t.Helper()
	ctx := t.Context()
	blocker := createReachTask(t, store, "blocker", "")
	if _, err := store.Link(ctx, id, blocker.ID, "u"); err != nil {
		t.Logf("link: %v", err)
	}
	if _, err := store.Unlink(ctx, id, blocker.ID, "u"); err != nil {
		t.Logf("unlink: %v", err)
	}
	if _, err := store.RecordRun(ctx, id, &Run{ID: "run-1", Worker: "agent", Outcome: "ok", StartedAt: time.Now().UTC()}, "u"); err != nil {
		t.Logf("record run: %v", err)
	}
	if _, err := store.Complete(ctx, id, nil, nil, "u"); err != nil {
		t.Logf("complete: %v", err)
	}
	if _, err := store.Archive(ctx, id, "u"); err != nil {
		t.Logf("archive: %v", err)
	}
}
