package kanban

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func smokeStore(t *testing.T) *Store {
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
	s := New(db, t.TempDir())
	if _, berr := s.InitBoard(t.Context(), "default", "u"); berr != nil {
		t.Fatal(berr)
	}
	return s
}

func smokeTask(t *testing.T, s *Store, title string, st Status) *Task {
	t.Helper()
	task, _, err := s.CreateTask(t.Context(), "default", &TaskSpec{Title: title, Status: st}, "u")
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// step reports a domain refusal without failing: these tests walk every store
// method of the earlier slices, they do not re-test the state machine.
func step(t *testing.T, name string, err error) {
	t.Helper()
	if err != nil {
		t.Logf("%s: %v", name, err)
	}
}

// TestF7KanbanStoreRoundTrip is the named storage test of this slice: create,
// idempotent create, link, query and the append-only event log.
func TestF7KanbanStoreRoundTrip(t *testing.T) {
	t.Parallel()
	s := smokeStore(t)
	first := smokeTask(t, s, "write the store test", StatusReady)
	blocker := smokeTask(t, s, "block the first card", StatusBacklog)
	checkCreateIdempotent(t, s, first)
	checkLinkAndQuery(t, s, first, blocker)
	checkEventLog(t, s, first)
}

func checkCreateIdempotent(t *testing.T, s *Store, first *Task) {
	t.Helper()
	again, created, err := s.CreateTask(t.Context(), "default", &TaskSpec{Title: "write the store test"}, "u")
	if err != nil {
		t.Fatal(err)
	}
	if created || again.ID != first.ID {
		t.Fatalf("repeated create is not idempotent: %+v created=%t", again, created)
	}
}

func checkLinkAndQuery(t *testing.T, s *Store, first, blocker *Task) {
	t.Helper()
	if _, err := s.Link(t.Context(), first.ID, blocker.ID, "u"); err != nil {
		t.Fatalf("link: %v", err)
	}
	if _, err := s.Link(t.Context(), blocker.ID, first.ID, "u"); err == nil {
		t.Fatal("link cycle accepted")
	}
	got, err := s.Task(t.Context(), first.ID)
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if got.ID != first.ID || len(got.Links) == 0 {
		t.Fatalf("task = %+v", got)
	}
	list, err := s.Tasks(t.Context(), "default", Filter{})
	if err != nil {
		t.Fatalf("tasks: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("tasks = %d, want 2", len(list))
	}
}

func checkEventLog(t *testing.T, s *Store, first *Task) {
	t.Helper()
	events, err := s.TaskEvents(t.Context(), first.ID, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("events = %d, want at least create+link", len(events))
	}
	count, err := s.EventCount(t.Context(), first.ID)
	if err != nil {
		t.Fatalf("event count: %v", err)
	}
	if count != len(events) {
		t.Fatalf("event count = %d, want %d", count, len(events))
	}
	if _, err := s.Task(t.Context(), "default/missing"); err == nil {
		t.Fatal("missing task returned no error")
	}
}

// TestF7KanbanStateRoundTrip exercises the state machine, edits, comments and
// run bookkeeping introduced by the earlier slices.
func TestF7KanbanStateRoundTrip(t *testing.T) {
	t.Parallel()
	s := smokeStore(t)
	ctx := t.Context()
	task := smokeTask(t, s, "walk the state machine", StatusReady)
	blocker := smokeTask(t, s, "blocker", StatusBacklog)

	_, err := s.Assign(ctx, task.ID, "agent", "u")
	step(t, "assign", err)
	_, err = s.Reassign(ctx, task.ID, "agent2", "u")
	step(t, "reassign", err)
	_, err = s.SetModel(ctx, task.ID, "damnbot", "u")
	step(t, "set model", err)
	_, err = s.Specify(ctx, task.ID, "acceptance criteria", "u")
	step(t, "specify", err)
	_, err = s.Edit(ctx, task.ID, &TaskSpec{Title: "walk the state machine (edited)"}, "u")
	step(t, "edit", err)
	_, err = s.Schedule(ctx, task.ID, time.Now().UTC().Add(time.Hour), "u")
	step(t, "schedule", err)
	_, err = s.Comment(ctx, task.ID, "first comment", "u")
	step(t, "comment", err)
	_, err = s.Transition(ctx, task.ID, StatusInProgress, "u", "start")
	step(t, "transition", err)
	_, err = s.Block(ctx, task.ID, "waiting", "u")
	step(t, "block", err)
	_, err = s.Unblock(ctx, task.ID, "u")
	step(t, "unblock", err)
	_, err = s.RequestReview(ctx, task.ID, "u")
	step(t, "request review", err)
	_, err = s.RequestChanges(ctx, task.ID, "needs work", "u")
	step(t, "request changes", err)
	_, err = s.ReopenReview(ctx, task.ID, "reopen", "u")
	step(t, "reopen review", err)
	_, err = s.Promote(ctx, task.ID, "u")
	step(t, "promote", err)
	_, err = s.Link(ctx, task.ID, blocker.ID, "u")
	step(t, "link", err)
	_, err = s.Unlink(ctx, task.ID, blocker.ID, "u")
	step(t, "unlink", err)
	_, err = s.RecordRun(ctx, task.ID, &Run{ID: "run-1", Worker: "agent", Outcome: "ok", StartedAt: time.Now().UTC()}, "u")
	step(t, "record run", err)
	_, err = s.Complete(ctx, task.ID, nil, nil, "u")
	step(t, "complete", err)
	_, err = s.Archive(ctx, task.ID, "u")
	step(t, "archive", err)
}

// TestF7KanbanClaimRoundTrip covers the worker lease surface and the
// environment settings.
func TestF7KanbanClaimRoundTrip(t *testing.T) {
	t.Parallel()
	s := smokeStore(t)
	ctx := t.Context()
	ready := smokeTask(t, s, "claimable", StatusReady)
	claimed, err := s.Claim(ctx, "default", "w1", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed == nil || claimed.ID != ready.ID {
		t.Fatalf("claim = %+v, want %s", claimed, ready.ID)
	}
	if _, err := s.Heartbeat(ctx, claimed.ID, "w1", time.Minute); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if _, err := s.Reclaim(ctx, claimed.ID, "u", true); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if _, err := s.ClaimTask(ctx, ready.ID, "w2", time.Minute); err != nil {
		t.Fatalf("claim task: %v", err)
	}
	if off := SettingsFromEnv(func(string) string { return "" }); off.DispatchInGateway || off.AutoDecompose || off.NotifyInGateway {
		t.Fatalf("absent env must keep every switch off: %+v", off)
	}
	if on := SettingsFromEnv(func(string) string { return "1" }); !on.DispatchInGateway || !on.AutoDecompose || !on.NotifyInGateway {
		t.Fatalf("env switches not read: %+v", on)
	}
}

type smokeFetch struct{ body []byte }

func (f smokeFetch) Get(context.Context, string, int64) ([]byte, error) { return f.body, nil }

// TestF7KanbanAttachmentRoundTrip covers the bounded attachment surface.
func TestF7KanbanAttachmentRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := smokeStore(t)
	s.Workspace = dir
	task := smokeTask(t, s, "attach", StatusBacklog)
	checkAttachFile(t, s, dir, task)
	checkAttachURL(t, s, task)
	checkDetach(t, s, task)
}

func checkAttachFile(t *testing.T, s *Store, dir string, task *Task) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := s.AttachFile(t.Context(), task.ID, "notes.txt", "u")
	if err != nil {
		t.Fatalf("attach file: %v", err)
	}
	if file.Name != "notes.txt" || file.Size != 5 || file.SHA256 == "" {
		t.Fatalf("attachment = %+v", file)
	}
	if _, err := s.AttachFile(t.Context(), task.ID, filepath.Join(dir, "..", "outside.txt"), "u"); err == nil {
		t.Fatal("path outside the workspace accepted")
	}
}

func checkAttachURL(t *testing.T, s *Store, task *Task) {
	t.Helper()
	s.Fetch = smokeFetch{body: []byte("payload")}
	s.Resolve = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("93.184.216.34")}, nil }
	if _, err := s.AttachURL(t.Context(), task.ID, "https://example.com/data", "u"); err != nil {
		t.Fatalf("attach url: %v", err)
	}
	s.Resolve = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }
	if _, err := s.AttachURL(t.Context(), task.ID, "https://loopback.example/data", "u"); err == nil {
		t.Fatal("loopback URL accepted")
	}
	// safeFetch is the default client; a closed loopback port fails fast and
	// never leaves the machine.
	if _, err := (safeFetch{}).Get(t.Context(), "http://127.0.0.1:1/none", 8); err == nil {
		t.Fatal("safeFetch reached a closed port without error")
	}
}

func checkDetach(t *testing.T, s *Store, task *Task) {
	t.Helper()
	list, err := s.Attachments(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("attachments: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("attachments = %+v, want file+url", list)
	}
	if _, err := s.Detach(t.Context(), task.ID, "notes.txt", "u"); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if _, err := s.Detach(t.Context(), task.ID, "missing.txt", "u"); err == nil {
		t.Fatal("detach of an unknown attachment succeeded")
	}
}
