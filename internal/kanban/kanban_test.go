package kanban

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func openDB(t *testing.T) *ctxdb.Store {
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
	return db
}

func baseTime() time.Time { return time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC) }

func newStore(t *testing.T) *Store {
	t.Helper()
	db := openDB(t)
	s := New(db, t.TempDir())
	now := baseTime()
	s.Now = func() time.Time { return now }
	return s
}

func mustBoard(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.InitBoard(t.Context(), "default", "owner"); err != nil {
		t.Fatal(err)
	}
}

func create(t *testing.T, s *Store, title string, status Status) *Task {
	t.Helper()
	mustBoard(t, s)
	task, _, err := s.CreateTask(t.Context(), "default", &TaskSpec{Title: title, Status: status}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// TestF7CLI029Kanban is the store-backed CLI-029 row: the shared Invocation
// drives the board, a repeat create is idempotent on (board, slug), and an
// unknown subcommand is rejected.
func TestF7CLI029Kanban(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	mustBoard(t, s)
	inv := &Invocation{Store: s, Board: "default", Actor: "cli"}
	if _, err := inv.Run(t.Context(), "init", []string{"--name", "default"}); err != nil {
		t.Fatal(err)
	}
	first, err := inv.Run(t.Context(), "create", []string{"--title", "Ship board", "--body", "spec", "--priority", "1", "--label", "p"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := inv.Run(t.Context(), "create", []string{"--title", "Ship board"})
	if err != nil {
		t.Fatal(err)
	}
	firstMap, ok := first.(map[string]any)
	if !ok {
		t.Fatalf("create result type %T", first)
	}
	created := false
	if v, vok := firstMap["created"].(bool); vok {
		created = v
	}
	againMap, ok := again.(map[string]any)
	if !ok {
		t.Fatalf("create result type %T", again)
	}
	second := false
	if v, vok := againMap["created"].(bool); vok {
		second = v
	}
	if !created || second {
		t.Fatalf("create idempotency: first=%v second=%v", created, again)
	}
	list, err := inv.Run(t.Context(), "list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if tasks, ok := list.([]Task); !ok || len(tasks) != 1 {
		t.Fatalf("list = %v", list)
	}
	if _, err := inv.Run(t.Context(), "not-a-command", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown subcommand err = %v", err)
	}
}

// fakeDecomposer returns one deterministic child per call.
type fakeDecomposer struct {
	calls int
	plan  Plan
	err   error
}

func (f *fakeDecomposer) Decompose(context.Context, *Task, int) (Plan, Usage, error) {
	f.calls++
	if f.plan.Children == nil {
		f.plan = Plan{Children: []ChildSpec{{Title: "decomposed child"}}}
	}
	return f.plan, Usage{RunID: "dec-1", CostMicros: 7}, f.err
}

// TestF7CLI029KanbanSubcommands drives every one of the 45 canonical
// subcommands through Invocation with a valid argument shape.
func TestF7CLI029KanbanSubcommands(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	mustBoard(t, s)
	inv := &Invocation{Store: s, Board: "default", Actor: "cli"}
	if err := os.WriteFile(filepath.Join(s.Workspace, "note.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	ran := map[string]bool{}
	must := func(sub string, args ...string) any {
		t.Helper()
		v, err := inv.Run(t.Context(), sub, args)
		if err != nil {
			t.Fatalf("%s: %v", sub, err)
		}
		ran[sub] = true
		return v
	}
	exerciseTasks(t, must, inv)
	exerciseAutomation(t, must, inv)
	if len(ran) != len(Subcommands()) {
		t.Fatalf("exercised %d/%d subcommands", len(ran), len(Subcommands()))
	}
}

func taskFrom(t *testing.T, v any) *Task {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("result type %T", v)
	}
	task, ok := m["task"].(*Task)
	if !ok {
		t.Fatalf("task type %T", m["task"])
	}
	return task
}

func exerciseTasks(t *testing.T, must func(string, ...string) any, inv *Invocation) {
	t.Helper()
	must("init", "--name", "second")
	must("boards")
	must("create", "--title", "main", "--body", "work")
	child := taskFrom(t, must("create", "--title", "child", "--status", "ready"))
	must("list")
	must("show", child.ID)
	must("assign", "--assignee", "w1", child.ID)
	must("set-model", "--model", "m1", child.ID)
	must("reassign", "--assignee", "w2", child.ID)
	must("link", "--dep", child.ID, "b-default/main")
	must("unlink", "--dep", child.ID, "b-default/main")
	must("claim", "--worker", "w2", child.ID)
	must("heartbeat", "--worker", "w2", child.ID)
	must("comment", "--text", "progress", child.ID)
	must("attach", "--path", "note.txt", child.ID)
	must("attachments", child.ID)
	must("attach-rm", "--name", "note.txt", child.ID)
	must("request-review", child.ID)
	must("request-changes", "--reason", "fix", child.ID)
	must("request-review", child.ID)
	must("reopen-review", child.ID)
	must("block", "--reason", "waiting", child.ID)
	must("unblock", child.ID)
	must("claim", "--worker", "w2", child.ID)
	must("reclaim", "--force", child.ID)
	must("claim", "--worker", "w2", child.ID)
	must("schedule", "--at", baseTime().Add(time.Hour).Format(time.RFC3339), child.ID)
	must("specify", "--text", "more spec", child.ID)
	must("edit", "--title", "child2", "--priority", "3", child.ID)
	must("complete", "--summary", "done", "--verify", "make test", child.ID)
	must("archive", child.ID)
	must("promote", "b-default/main")
	_ = inv
}

func exerciseAutomation(t *testing.T, must func(string, ...string) any, inv *Invocation) {
	t.Helper()
	big := taskFrom(t, must("create", "--title", "big", "--status", "ready"))
	inv.Decomposer = &fakeDecomposer{}
	must("decompose", "--max", "2", big.ID)
	parent := taskFrom(t, must("create", "--title", "swarm parent", "--status", "ready"))
	must("swarm", "--profile", "a", "--profile", "b", "--title", "s1", "--title", "s2", parent.ID)
	inv.Runner = RunnerFunc(func(context.Context, *Task, string) (WorkerOutcome, error) {
		return WorkerOutcome{Handoff: Handoff{Summary: "ok"}}, nil
	})
	must("create", "--title", "dispatch me", "--status", "ready")
	must("dispatch", "--worker", "w1")
	must("create", "--title", "daemon me", "--status", "ready")
	must("daemon", "--once", "--worker", "w1")
	must("tail", "--limit", "5")
	must("watch", "--limit", "5")
	must("stats")
	must("diagnostics")
	must("log", "--limit", "5")
	must("runs")
	must("assignees")
	must("context")
	must("notify-subscribe", "--target", "telegram:1")
	must("notify-list")
	must("notify-unsubscribe", "--target", "telegram:1")
	must("gc")
	must("repair")
}
