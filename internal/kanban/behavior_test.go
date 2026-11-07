package kanban

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/tools"
	"github.com/damnbot/damnbot/internal/toolset"
)

// --- state machine and leases ---

func TestF7KanbanStateMachineRejectsIllegal(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	task := create(t, s, "state", StatusBacklog)
	if _, err := s.RequestReview(t.Context(), task.ID, "u"); !errors.Is(err, ErrTransition) {
		t.Fatalf("backlog->review err = %v", err)
	}
	if _, err := s.Complete(t.Context(), task.ID, &Handoff{Summary: "x"}, nil, "u"); !errors.Is(err, ErrTransition) {
		t.Fatalf("backlog->done err = %v", err)
	}
	if _, err := s.Archive(t.Context(), task.ID, "u"); !errors.Is(err, ErrTransition) {
		t.Fatalf("archive before done err = %v", err)
	}
	if _, err := s.Transition(t.Context(), task.ID, Status("bogus"), "u", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown status err = %v", err)
	}
}

func TestF7KanbanDispatchLease(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	task := create(t, s, "lease", StatusReady)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, losses := 0, 0
	for range 8 {
		wg.Go(func() {
			if _, err := s.ClaimTask(context.Background(), task.ID, "w", time.Minute); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			} else if errors.Is(err, ErrClaimed) {
				mu.Lock()
				losses++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if wins != 1 || losses != 7 {
		t.Fatalf("claims: wins=%d losses=%d", wins, losses)
	}
}

func TestF7KanbanHeartbeatTimeout(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	now := baseTime()
	s.Now = func() time.Time { return now }
	task := create(t, s, "heart", StatusReady)
	if _, err := s.ClaimTask(t.Context(), task.ID, "w", time.Minute); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	got, err := s.Task(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.NeedsAttention {
		t.Fatal("expired lease did not raise needs_attention")
	}
	if _, err := s.Heartbeat(t.Context(), task.ID, "w", 0); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("heartbeat after expiry err = %v", err)
	}
	if _, err := s.Reclaim(t.Context(), task.ID, "owner", false); err != nil {
		t.Fatal(err)
	}
}

// --- attachments and SSRF ---

type countingFetch struct {
	calls int
	body  []byte
}

func (f *countingFetch) Get(context.Context, string, int64) ([]byte, error) {
	f.calls++
	return f.body, nil
}

func TestF7KanbanAttachmentOutside(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	task := create(t, s, "attach", StatusReady)
	if _, err := s.AttachFile(t.Context(), task.ID, "../escape.txt", "u"); !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("outside err = %v", err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(s.Workspace, "link.txt")
	if err := os.Symlink(outside, link); err == nil {
		if _, err := s.AttachFile(t.Context(), task.ID, "link.txt", "u"); !errors.Is(err, ErrOutsideWorkspace) {
			t.Fatalf("symlink escape err = %v", err)
		}
	}
}

func TestF7KanbanURLSSRF(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	task := create(t, s, "url", StatusReady)
	fetch := &countingFetch{body: []byte("ok")}
	s.Fetch = fetch
	s.Resolve = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }
	if _, err := s.AttachURL(t.Context(), task.ID, "https://example.com/x", "u"); !errors.Is(err, ErrBlockedURL) {
		t.Fatalf("loopback err = %v", err)
	}
	s.Resolve = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("169.254.169.254")}, nil }
	if _, err := s.AttachURL(t.Context(), task.ID, "https://metadata.example/x", "u"); !errors.Is(err, ErrBlockedURL) {
		t.Fatalf("metadata err = %v", err)
	}
	if fetch.calls != 0 {
		t.Fatalf("fetch ran %d times before validation", fetch.calls)
	}
	s.Resolve = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("93.184.216.34")}, nil }
	if _, err := s.AttachURL(t.Context(), task.ID, "http://example.com/x", "u"); !errors.Is(err, ErrBlockedURL) {
		t.Fatalf("http err = %v", err)
	}
	att, err := s.AttachURL(t.Context(), task.ID, "https://example.com/x", "u")
	if err != nil || att.Kind != "url" || fetch.calls != 1 {
		t.Fatalf("https attach = %+v, %v calls=%d", att, err, fetch.calls)
	}
	fetch.body = make([]byte, s.MaxAttach+1)
	if _, err := s.AttachURL(t.Context(), task.ID, "https://example.com/big", "u"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize err = %v", err)
	}
}

// --- cost once ---

func TestF7KanbanCostOnce(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	task := create(t, s, "cost", StatusReady)
	if fresh, err := s.RecordRun(t.Context(), task.ID, &Run{ID: "r1", CostMicros: 5}, "u"); err != nil || !fresh {
		t.Fatalf("first run = %v, %v", fresh, err)
	}
	if fresh, err := s.RecordRun(t.Context(), task.ID, &Run{ID: "r1", CostMicros: 5}, "u"); err != nil || fresh {
		t.Fatalf("second run = %v, %v", fresh, err)
	}
	got, err := s.Task(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Runs) != 1 || got.Runs[0].CostMicros != 5 {
		t.Fatalf("runs = %+v", got.Runs)
	}
}

// --- dispatch and dependency gating ---

func TestF7AUTO017KanbanDispatch(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	a := create(t, s, "dispatch a", StatusReady)
	b := create(t, s, "dispatch b", StatusReady)
	parent := create(t, s, "parent", StatusBacklog)
	child, _, err := s.CreateTask(t.Context(), "default", &TaskSpec{Title: "gated", Status: StatusReady, Links: []string{parent.ID}}, "u")
	if err != nil {
		t.Fatal(err)
	}
	var ran []string
	d := &Dispatcher{Store: s, Board: "default", Workers: []string{"w1"}, Runner: RunnerFunc(func(_ context.Context, task *Task, _ string) (WorkerOutcome, error) {
		ran = append(ran, task.ID)
		return WorkerOutcome{Handoff: Handoff{Summary: "ok"}}, nil
	})}
	for range 3 {
		if _, err := d.DispatchOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{a.ID, b.ID} {
		requireStatus(t, s, id, StatusDone)
	}
	if len(ran) != 2 {
		t.Fatalf("ran = %v (gated child must wait)", ran)
	}
	requireStatus(t, s, child.ID, StatusReady)
	if _, err := s.Promote(t.Context(), parent.ID, "u"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimTask(t.Context(), parent.ID, "w1", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(t.Context(), parent.ID, &Handoff{Summary: "ok"}, nil, "u"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DispatchOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, s, child.ID, StatusDone)
}

func requireStatus(t *testing.T, s *Store, id string, want Status) {
	t.Helper()
	got, err := s.Task(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != want {
		t.Fatalf("%s status = %s, want %s", id, got.Status, want)
	}
}

// --- auto-decompose ---

func TestF7AUTO018KanbanDecompose(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	task := create(t, s, "big", StatusReady)
	dec := &fakeDecomposer{plan: Plan{Children: []ChildSpec{{Title: "child one"}, {Title: "child two"}}}}
	children, err := s.Decompose(t.Context(), task.ID, dec, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 || dec.calls != 1 {
		t.Fatalf("children=%d calls=%d", len(children), dec.calls)
	}
	again, err := s.Decompose(t.Context(), task.ID, dec, 4)
	if err != nil || len(again) != 2 {
		t.Fatalf("repeat = %v, %v", again, err)
	}
	got, gerr := s.Task(t.Context(), task.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if len(got.Runs) != 1 {
		t.Fatalf("cost recorded %d times", len(got.Runs))
	}
	if _, err := s.Decompose(t.Context(), task.ID, emptyDecomposer{}, 4); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty plan err = %v", err)
	}
	if _, err := s.Decompose(t.Context(), task.ID, nil, 4); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("nil decomposer err = %v", err)
	}
}

type emptyDecomposer struct{}

func (emptyDecomposer) Decompose(context.Context, *Task, int) (Plan, Usage, error) {
	return Plan{}, Usage{RunID: "empty"}, nil
}

// --- notifier ---

func TestF7AUTO020KanbanSwarm(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	parent := create(t, s, "swarm parent", StatusReady)
	kids, err := s.Swarm(t.Context(), parent.ID, SwarmPlan{
		Profiles: []string{"a", "b"},
		Tasks:    []ChildSpec{{Title: "s1"}, {Title: "s2"}},
	}, "owner")
	if err != nil || len(kids) != 2 {
		t.Fatalf("swarm = %v, %v", kids, err)
	}
	if kids[0].Assignee != "a" || kids[1].Assignee != "b" {
		t.Fatalf("assignees = %s,%s", kids[0].Assignee, kids[1].Assignee)
	}
	steps, err := s.Workflow(t.Context(), parent.ID, []ChildSpec{{Title: "w1"}, {Title: "w2"}}, "owner")
	if err != nil || len(steps) != 2 {
		t.Fatalf("workflow = %v, %v", steps, err)
	}
	if len(steps[1].Links) != 1 || steps[1].Links[0] != steps[0].ID {
		t.Fatalf("workflow links = %v", steps[1].Links)
	}
	if _, aerr := s.AcceptPR(t.Context(), parent.ID, "pr-1", []Evidence{{Kind: "test", Name: "x", Passed: true}}, "u"); !errors.Is(aerr, ErrTransition) {
		t.Fatalf("accept before done err = %v", aerr)
	}
	run := &Run{ID: "r-pr", Worker: "w", Outcome: "ok", CostMicros: 3}
	if _, cerr := s.ClaimTask(t.Context(), parent.ID, "worker", time.Minute); cerr != nil {
		t.Fatal(cerr)
	}
	if _, cerr := s.Complete(t.Context(), parent.ID, &Handoff{Summary: "ok", Verify: []string{"test"}}, run, "worker"); cerr != nil {
		t.Fatal(cerr)
	}
	if _, verr := s.AcceptPR(t.Context(), parent.ID, "pr-1", []Evidence{{Kind: "test", Name: "x", Passed: false}}, "u"); !errors.Is(verr, ErrInvalid) {
		t.Fatalf("failed evidence accepted: %v", verr)
	}
	accepted, err := s.AcceptPR(t.Context(), parent.ID, "pr-1", []Evidence{{Kind: "test", Name: "x", Passed: true}}, "u")
	if err != nil {
		t.Fatal(err)
	}
	if !MergeAllowed(accepted) {
		t.Fatal("merge not allowed after acceptance")
	}
}

// --- plugin ---

func TestF7PLUG021KanbanDashboard(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	mustBoard(t, s)
	task, _, err := s.CreateTask(t.Context(), "default", &TaskSpec{Title: "dash", Body: "secret body", Status: StatusReady}, "u")
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.DashboardData(t.Context(), "default")
	if err != nil || len(d.Boards) != 1 || len(d.Tasks) != 1 {
		t.Fatalf("dashboard = %+v, %v", d, err)
	}
	raw, merr := json.Marshal(d)
	if merr != nil {
		t.Fatal(merr)
	}
	if strings.Contains(string(raw), "secret body") || strings.Contains(string(raw), task.Body) {
		t.Fatalf("dashboard leaked body: %s", raw)
	}
	unit, err := SystemdUnit(UnitOptions{Binary: "/usr/bin/damnbot", Data: "/var/lib/damnbot"})
	if err != nil || !strings.Contains(unit, "ExecStart=/usr/bin/damnbot kanban daemon") {
		t.Fatalf("unit = %q, %v", unit, err)
	}
	if _, err := SystemdUnit(UnitOptions{Binary: "/opt/secret-tool"}); err == nil {
		t.Fatal("secret path accepted")
	}
}

// --- tools ---

func newToolSet(t *testing.T) (*Store, *Task, map[string]tools.Tool) {
	t.Helper()
	s := newStore(t)
	task := create(t, s, "tool", StatusReady)
	list := NewTools(s, "default", task.ID, "agent", true)
	m := make(map[string]tools.Tool, len(list))
	for _, tl := range list {
		m[tl.Name()] = tl
	}
	return s, task, m
}

func callTool(t *testing.T, tl tools.Tool, env tools.Env, args string) (tools.Result, error) {
	t.Helper()
	res, err := tl.Run(t.Context(), env, json.RawMessage(args))
	if err != nil {
		return res, fmt.Errorf("tool %s: %w", tl.Name(), err)
	}
	return res, nil
}

func codeOf(err error) tools.Code {
	if te, ok := errors.AsType[*tools.ToolError](err); ok {
		return te.Code
	}
	return ""
}

func TestF7TOOL055KanbanAttach(t *testing.T) {
	t.Parallel()
	s, task, m := newToolSet(t)
	if err := os.WriteFile(filepath.Join(s.Workspace, "in.txt"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := tools.Env{Cwd: s.Workspace}
	if _, err := callTool(t, m["kanban_attach"], env, `{"path":"in.txt"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(t, m["kanban_attach"], env, `{"path":"../out.txt"}`); codeOf(err) != tools.CodeOutsideWorkspace {
		t.Fatalf("outside err = %v (%s)", err, codeOf(err))
	}
	got, gerr := s.Task(t.Context(), task.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if len(got.Attachments) != 1 {
		t.Fatalf("attachments = %+v", got.Attachments)
	}
}

func TestF7TOOL056KanbanAttachURL(t *testing.T) {
	t.Parallel()
	s, _, m := newToolSet(t)
	fetch := &countingFetch{body: []byte("body")}
	s.Fetch = fetch
	s.Resolve = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("10.0.0.1")}, nil }
	if _, err := callTool(t, m["kanban_attach_url"], tools.Env{}, `{"url":"https://private.example/x"}`); codeOf(err) != tools.CodeBlockedHost {
		t.Fatalf("private err = %v (%s)", err, codeOf(err))
	}
	if fetch.calls != 0 {
		t.Fatal("fetched before SSRF gate")
	}
}

func TestF7TOOL057KanbanAttachments(t *testing.T) {
	t.Parallel()
	_, _, m := newToolSet(t)
	res, err := callTool(t, m["kanban_attachments"], tools.Env{}, `{}`)
	if err != nil || !strings.Contains(res.Content, "[]") {
		t.Fatalf("attachments = %q, %v", res.Content, err)
	}
}

func TestF7TOOL058KanbanBlock(t *testing.T) {
	t.Parallel()
	s, task, m := newToolSet(t)
	if _, err := callTool(t, m["kanban_block"], tools.Env{}, `{"reason":""}`); codeOf(err) != tools.CodeInvalidArgs {
		t.Fatalf("empty reason err = %v", err)
	}
	if _, err := callTool(t, m["kanban_block"], tools.Env{}, `{"reason":"waiting"}`); err != nil {
		t.Fatal(err)
	}
	got, gerr := s.Task(t.Context(), task.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if got.Status != StatusBlocked {
		t.Fatalf("status = %s", got.Status)
	}
	if _, err := callTool(t, m["kanban_request_review"], tools.Env{}, `{}`); codeOf(err) != tools.CodeDenied {
		t.Fatalf("illegal transition through tool = %v", err)
	}
}

func TestF7TOOL059KanbanComment(t *testing.T) {
	t.Parallel()
	s, task, m := newToolSet(t)
	if _, err := callTool(t, m["kanban_comment"], tools.Env{}, `{"text":"first"}`); err != nil {
		t.Fatal(err)
	}
	before, berr := s.EventCount(t.Context(), task.ID)
	if berr != nil {
		t.Fatal(berr)
	}
	err := s.DB.Write(t.Context(), func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`UPDATE kanban_task_events SET payload = '{}' WHERE task_id = ?1`, task.ID)
		if xerr != nil {
			return fmt.Errorf("update event: %w", xerr)
		}
		return nil
	})
	if err == nil {
		t.Fatal("event row was mutable")
	}
	after, aerr := s.EventCount(t.Context(), task.ID)
	if aerr != nil {
		t.Fatal(aerr)
	}
	if before != after {
		t.Fatal("append-only event count changed")
	}
}

func TestF7TOOL060KanbanComplete(t *testing.T) {
	t.Parallel()
	s, task, m := newToolSet(t)
	if _, err := s.ClaimTask(t.Context(), task.ID, "agent", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(t, m["kanban_complete"], tools.Env{}, `{"summary":""}`); codeOf(err) != tools.CodeInvalidArgs {
		t.Fatalf("empty handoff err = %v", err)
	}
	args := `{"summary":"done","verify":["test"],"run_id":"run-1","cost_micros":9}`
	if _, err := callTool(t, m["kanban_complete"], tools.Env{}, args); err != nil {
		t.Fatal(err)
	}
	got, gerr := s.Task(t.Context(), task.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if got.Status != StatusDone || len(got.Runs) != 1 || got.Runs[0].CostMicros != 9 {
		t.Fatalf("completed = %+v", got)
	}
}

func TestF7TOOL061KanbanCreate(t *testing.T) {
	t.Parallel()
	s, _, m := newToolSet(t)
	args := `{"title":"child","links":["b-default/tool"]}`
	res, err := callTool(t, m["kanban_create"], tools.Env{}, args)
	if err != nil || !strings.Contains(res.Content, `"created":true`) {
		t.Fatalf("create = %q, %v", res.Content, err)
	}
	res, err = callTool(t, m["kanban_create"], tools.Env{}, args)
	if err != nil || !strings.Contains(res.Content, `"created":false`) {
		t.Fatalf("repeat create = %q, %v", res.Content, err)
	}
	got, terr := s.Tasks(t.Context(), "default", Filter{Status: StatusReady})
	if terr != nil {
		t.Fatal(terr)
	}
	if len(got) < 2 {
		t.Fatalf("tasks = %d", len(got))
	}
}

func TestF7TOOL062KanbanHeartbeat(t *testing.T) {
	t.Parallel()
	s, task, m := newToolSet(t)
	if _, err := s.ClaimTask(t.Context(), task.ID, "agent", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(t, m["kanban_heartbeat"], tools.Env{}, `{}`); err != nil {
		t.Fatal(err)
	}
	now := baseTime()
	s.Now = func() time.Time { return now }
	now = now.Add(10 * time.Minute)
	got, gerr := s.Task(t.Context(), task.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if !got.NeedsAttention {
		t.Fatal("no needs_attention after lease timeout")
	}
}

func TestF7TOOL063KanbanLink(t *testing.T) {
	t.Parallel()
	s, task, m := newToolSet(t)
	other, _, err := s.CreateTask(t.Context(), "default", &TaskSpec{Title: "other", Status: StatusReady}, "u")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(t, m["kanban_link"], tools.Env{}, `{"dep":"`+other.ID+`"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(t, m["kanban_link"], tools.Env{}, `{"dep":"`+task.ID+`"}`); codeOf(err) != tools.CodeInvalidArgs {
		t.Fatalf("self cycle err = %v", err)
	}
	otherSet := NewTools(s, "default", other.ID, "agent", true)
	var cycle tools.Tool
	for _, tl := range otherSet {
		if tl.Name() == "kanban_link" {
			cycle = tl
		}
	}
	if _, err := callTool(t, cycle, tools.Env{}, `{"dep":"`+task.ID+`"}`); codeOf(err) != tools.CodeInvalidArgs {
		t.Fatalf("two-node cycle err = %v", err)
	}
}

func TestF7TOOL064KanbanList(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	create(t, s, "listed", StatusReady)
	list := NewTools(s, "default", "", "agent", false)
	var denied bool
	for _, tl := range list {
		if tl.Name() != "kanban_list" {
			continue
		}
		if _, err := callTool(t, tl, tools.Env{}, `{}`); codeOf(err) == tools.CodeDenied {
			denied = true
		}
	}
	if !denied {
		t.Fatal("kanban_list was available without orchestrator mode")
	}
}

func TestF7TOOL065KanbanRequestChanges(t *testing.T) {
	t.Parallel()
	s, task, m := newToolSet(t)
	if _, err := s.ClaimTask(t.Context(), task.ID, "agent", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(t, m["kanban_request_review"], tools.Env{}, `{}`); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(t, m["kanban_request_changes"], tools.Env{}, `{"reason":""}`); codeOf(err) != tools.CodeInvalidArgs {
		t.Fatalf("empty reason err = %v", err)
	}
	if _, err := callTool(t, m["kanban_request_changes"], tools.Env{}, `{"reason":"fix"}`); err != nil {
		t.Fatal(err)
	}
	got, gerr := s.Task(t.Context(), task.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if got.Status != StatusInProgress {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestF7TOOL066KanbanRequestReview(t *testing.T) {
	t.Parallel()
	s, task, m := newToolSet(t)
	if _, err := callTool(t, m["kanban_request_review"], tools.Env{}, `{}`); codeOf(err) != tools.CodeDenied {
		t.Fatalf("ready->review err = %v", err)
	}
	if _, err := s.ClaimTask(t.Context(), task.ID, "agent", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(t, m["kanban_request_review"], tools.Env{}, `{}`); err != nil {
		t.Fatal(err)
	}
	got, gerr := s.Task(t.Context(), task.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if got.Status != StatusReview {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestF7TOOL067KanbanSchedule(t *testing.T) {
	t.Parallel()
	s, task, m := newToolSet(t)
	if _, err := callTool(t, m["kanban_schedule"], tools.Env{}, `{"at":"2020-01-01T00:00:00Z"}`); codeOf(err) != tools.CodeInvalidArgs {
		t.Fatalf("past schedule err = %v", err)
	}
	at := baseTime().Add(2 * time.Hour).Format(time.RFC3339)
	if _, err := callTool(t, m["kanban_schedule"], tools.Env{}, `{"at":"`+at+`"}`); err != nil {
		t.Fatal(err)
	}
	got, gerr := s.Task(t.Context(), task.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if got.DueAt.IsZero() {
		t.Fatal("due_at not stored")
	}
}

func TestF7TOOL068KanbanShow(t *testing.T) {
	t.Parallel()
	_, task, m := newToolSet(t)
	if _, err := callTool(t, m["kanban_comment"], tools.Env{}, `{"text":"visible"}`); err != nil {
		t.Fatal(err)
	}
	res, err := callTool(t, m["kanban_show"], tools.Env{}, `{}`)
	if err != nil || !strings.Contains(res.Content, task.ID) || !strings.Contains(res.Content, "visible") {
		t.Fatalf("show = %q, %v", res.Content, err)
	}
	if _, err := callTool(t, m["kanban_show"], tools.Env{}, `{"task_id":"missing"}`); codeOf(err) != tools.CodeNotFound {
		t.Fatalf("missing err = %v", err)
	}
}

func TestF7TOOL069KanbanUnblock(t *testing.T) {
	t.Parallel()
	s, task, m := newToolSet(t)
	if _, err := callTool(t, m["kanban_block"], tools.Env{}, `{"reason":"wait"}`); err != nil {
		t.Fatal(err)
	}
	nonOrch := NewTools(s, "default", task.ID, "agent", false)
	var denied bool
	for _, tl := range nonOrch {
		if tl.Name() == "kanban_unblock" {
			_, err := callTool(t, tl, tools.Env{}, `{}`)
			denied = codeOf(err) == tools.CodeDenied
		}
	}
	if !denied {
		t.Fatal("unblock allowed without orchestrator")
	}
	if _, err := callTool(t, m["kanban_unblock"], tools.Env{}, `{}`); err != nil {
		t.Fatal(err)
	}
	got, gerr := s.Task(t.Context(), task.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if got.Status != StatusReady {
		t.Fatalf("status = %s", got.Status)
	}
}

// TestF7TSET020Kanban proves the declarative toolset: 15 tools, default off
// (not part of hermes-cli), available with an explicit selection even when
// the inventory flag is false.
func TestF7TSET020Kanban(t *testing.T) {
	t.Parallel()
	var def *toolset.Definition
	for _, d := range toolset.Builtin() {
		if d.Name == "kanban" {
			dd := d
			def = &dd
		}
	}
	if def == nil || len(def.Tools) != 15 {
		t.Fatalf("kanban toolset = %+v", def)
	}
	available := map[string]bool{}
	for _, name := range def.Tools {
		available[name] = true
	}
	sel, err := toolset.Resolve(toolset.Config{Active: []string{"kanban"}}, "linux", available)
	if err != nil || len(sel.Tools) != 15 {
		t.Fatalf("selected = %v, %v", sel.Tools, err)
	}
	declared := toolset.DeclaredTools()
	for _, name := range def.Tools {
		if !declared[name] {
			t.Fatalf("%s not declared", name)
		}
	}
}
