package kanban

import (
	"context"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway/render"
)

// TestF7KanbanSwarmRoundTrip is the named swarm/workflow/PR-acceptance test of
// this slice: it walks Swarm, Workflow and AcceptPR so the surface stays
// reachable (and so the acceptance rules are pinned).
func TestF7KanbanSwarmRoundTrip(t *testing.T) {
	t.Parallel()
	s := smokeStore(t)
	parent := smokeTask(t, s, "orchestrate the release", StatusReady)
	checkSwarmAndWorkflow(t, s, parent)
	checkAcceptance(t, s)
	checkEvidenceGate(t, s)
}

func checkSwarmAndWorkflow(t *testing.T, s *Store, parent *Task) {
	t.Helper()
	ctx := t.Context()
	children, err := s.Swarm(ctx, parent.ID, SwarmPlan{
		Profiles: []string{"agent-a", "agent-b"},
		Tasks: []ChildSpec{
			{Title: "first fan-out", Assignee: "agent-a"},
			{Title: "second fan-out", Assignee: "agent-b"},
		},
	}, "u")
	if err != nil {
		t.Fatalf("swarm: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("swarm children = %d, want 2", len(children))
	}
	steps, err := s.Workflow(ctx, parent.ID, []ChildSpec{{Title: "step one"}, {Title: "step two"}}, "u")
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("workflow steps = %d, want 2", len(steps))
	}
	if _, err := s.Workflow(ctx, parent.ID, nil, "u"); err == nil {
		t.Fatal("empty workflow accepted")
	}
	bc, cerr := s.Context(ctx, "default")
	if cerr != nil {
		t.Fatalf("board context: %v", cerr)
	}
	if len(bc.Tasks) == 0 {
		t.Fatal("board context returned no tasks")
	}
	if stats, aerr := s.Assignees(ctx, "default"); aerr != nil {
		t.Fatalf("assignees: %v", aerr)
	} else if len(stats) == 0 {
		t.Fatal("assignees returned no rows for a board with an assignee")
	}
	checkQuerySurface(t, s)
}

func checkQuerySurface(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	if _, err := s.Boards(ctx); err != nil {
		t.Fatalf("boards: %v", err)
	}
	if _, err := s.Subscribe(ctx, "default", "telegram:42"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if subs, err := s.Notifications(ctx, "default"); err != nil || len(subs) == 0 {
		t.Fatalf("notifications = %v, %v", subs, err)
	}
	if err := s.Unsubscribe(ctx, "default", "telegram:42"); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if _, err := s.DashboardData(ctx, "default"); err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	if _, err := s.Stats(ctx, "default"); err != nil {
		t.Fatalf("stats: %v", err)
	}
	if _, err := s.Diagnostics(ctx, "default"); err != nil {
		t.Fatalf("diagnostics: %v", err)
	}
	if _, err := s.Tail(ctx, "default", 0); err != nil {
		t.Fatalf("tail: %v", err)
	}
	if _, err := s.Runs(ctx, "default"); err != nil {
		t.Fatalf("runs: %v", err)
	}
	checkNotifier(t, s)
	checkDispatcher(t, s)
	if !UniqueChildren(mustTasks(t, s)) {
		t.Fatal("children are not unique")
	}
	if RunIDFor("b-default/x") == "" {
		t.Fatal("RunIDFor returned an empty deterministic id")
	}
	if _, err := s.GC(ctx, "default", time.Hour); err != nil {
		t.Fatalf("gc: %v", err)
	}
	if _, err := s.Repair(ctx, "default"); err != nil {
		t.Fatalf("repair: %v", err)
	}
}

type smokeSender struct{ sent int }

func (f *smokeSender) Send(context.Context, string, string, []render.Message) error {
	f.sent++
	return nil
}

func checkNotifier(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	if _, err := s.Subscribe(ctx, "default", "telegram:7"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	task := smokeTask(t, s, "notify **me**", StatusReady)
	sender := &smokeSender{}
	n := &Notifier{Store: s, Sender: sender}
	if err := n.Notify(ctx, task, "moved"); err != nil {
		t.Fatalf("notify: %v", err)
	}
	if sender.sent == 0 {
		t.Fatal("notifier sent nothing to a subscribed target")
	}
	if err := (&Notifier{Store: s}).Notify(ctx, task, "moved"); err == nil {
		t.Fatal("notifier without a sender accepted the call")
	}
	if unit, err := SystemdUnit(UnitOptions{}); err != nil || unit == "" {
		t.Fatalf("systemd unit = %q, %v", unit, err)
	}
}

func smokeRunner() Runner {
	return RunnerFunc(func(context.Context, *Task, string) (WorkerOutcome, error) {
		return WorkerOutcome{Outcome: "ok", Handoff: Handoff{Summary: "dispatched", Verify: []string{"test"}}}, nil
	})
}

func checkDispatcher(t *testing.T, s *Store) {
	t.Helper()
	d := &Dispatcher{Store: s, Board: "default", Workers: []string{"w1"}, Runner: smokeRunner()}
	if _, err := d.DispatchOnce(t.Context()); err != nil {
		t.Fatalf("dispatch once: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := d.Run(ctx, time.Millisecond); err != nil {
		t.Fatalf("dispatcher run: %v", err)
	}
}

func checkAcceptance(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	parent := smokeTask(t, s, "accept the pr", StatusReady)
	evidence := []Evidence{{Kind: "test", Name: "TestX", Passed: true}}
	if _, err := s.AcceptPR(ctx, parent.ID, "https://example.com/pr/1", evidence, "u"); err == nil {
		t.Fatal("AcceptPR accepted a card that is not done")
	}
	if _, err := s.Transition(ctx, parent.ID, StatusInProgress, "u", "start"); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if _, err := s.Complete(ctx, parent.ID, &Handoff{Summary: "workflow done", Verify: []string{"test"}}, &Run{ID: "run-swarm", Worker: "agent", Outcome: "ok", StartedAt: time.Now().UTC()}, "u"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if MergeAllowed(mustTaskByID(t, s, parent.ID)) {
		t.Fatal("MergeAllowed allowed a merge without a recorded acceptance")
	}
	if _, err := s.AcceptPR(ctx, parent.ID, "https://example.com/pr/1", evidence, "u"); err != nil {
		t.Fatalf("accept pr: %v", err)
	}
	if accepted := mustTaskByID(t, s, parent.ID); !MergeAllowed(accepted) {
		t.Fatalf("MergeAllowed refused an accepted card: %+v", accepted)
	}
}

func checkEvidenceGate(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	card := smokeTask(t, s, "reject the failing evidence", StatusReady)
	if _, err := s.Transition(ctx, card.ID, StatusInProgress, "u", "start"); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if _, err := s.Complete(ctx, card.ID, &Handoff{Summary: "no verification", Verify: []string{"test"}}, &Run{ID: "run-bad", Worker: "agent", Outcome: "ok", StartedAt: time.Now().UTC()}, "u"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := s.AcceptPR(ctx, card.ID, "https://example.com/pr/2", []Evidence{{Kind: "test", Name: "TestX", Passed: false}}, "u"); err == nil {
		t.Fatal("AcceptPR accepted failing evidence")
	}
}

func mustTasks(t *testing.T, s *Store) []*Task {
	t.Helper()
	tasks, err := s.Tasks(t.Context(), "default", Filter{})
	if err != nil {
		t.Fatalf("tasks: %v", err)
	}
	out := make([]*Task, 0, len(tasks))
	for i := range tasks {
		out = append(out, &tasks[i])
	}
	return out
}

func mustTaskByID(t *testing.T, s *Store, id string) *Task {
	t.Helper()
	task, err := s.Task(t.Context(), id)
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	return task
}
