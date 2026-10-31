package delegate_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/delegate"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/tools"
)

// fakeAgent is one deterministic subagent.
type fakeAgent struct {
	run func(context.Context, *delegate.Request) (delegate.AgentResult, error)
}

func (f fakeAgent) Run(ctx context.Context, req *delegate.Request) (delegate.AgentResult, error) {
	return f.run(ctx, req)
}

// memLedger records every rollup row.
type memLedger struct {
	mu   sync.Mutex
	rows []delegate.Rollup
}

func (m *memLedger) RecordDelegate(_ context.Context, r *delegate.Rollup) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, *r)
	return nil
}

func (m *memLedger) total(parent string) (int64, int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var micros, calls int64
	for _, r := range m.rows {
		if r.ParentSessionID == parent {
			micros += r.Usage.CostMicros
			calls += r.Usage.Calls
		}
	}
	return micros, calls
}

type fixedBudget float64

func (f fixedBudget) RemainingUSD(context.Context, string) (float64, error) { return float64(f), nil }

type fakeNotices struct {
	mu     sync.Mutex
	notifs []string
}

func (f *fakeNotices) Notice(_ context.Context, _, kind, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notifs = append(f.notifs, kind+": "+msg)
	return nil
}

func (f *fakeNotices) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.notifs)
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func view(t *testing.T, depth int) tools.SessionView {
	t.Helper()
	v, err := tools.NewSessionView("P", "", "run", "/w",
		[]string{"read_file", "write_file", "delegate_task"},
		[]tools.Capability{tools.CapDelegate, tools.CapNetwork})
	if err != nil {
		t.Fatal(err)
	}
	for range depth {
		v, err = v.Child(v.SessionID()+"x", v.Tools(), v.Capabilities())
		if err != nil {
			t.Fatal(err)
		}
	}
	return v
}

func TestDelegateBudgetSum(t *testing.T) {
	t.Parallel()
	led := &memLedger{}
	var peak atomic.Int64
	agents := func(_ context.Context, req *delegate.Request) (delegate.Agent, error) {
		return fakeAgent{run: func(context.Context, *delegate.Request) (delegate.AgentResult, error) {
			return delegate.AgentResult{
				Answer: "ok", StopReason: "end_turn",
				Usage: delegate.Usage{Input: 10, Output: 5, CostMicros: 100_000, Calls: 1},
			}, nil
		}}, nil
	}
	svc, err := delegate.New(&delegate.Config{MaxParallel: 2, MinCallPeriod: time.Nanosecond}, agents, led, fixedBudget(1.0), nil)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := svc.Run(context.Background(), tools.Env{TurnID: "T1", Session: view(t, 0)}, []delegate.Task{
		{Goal: "a"}, {Goal: "b"}, {Goal: "c"},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Reports) != 3 {
		t.Fatalf("reports = %d", len(batch.Reports))
	}
	micros, calls := led.total("P")
	if micros != 300_000 || calls != 3 {
		t.Fatalf("ledger rollup = %d micros / %d calls", micros, calls)
	}
	if batch.Usage.CostMicros != micros || math.Abs(batch.Spent-0.30) > 1e-9 {
		t.Fatalf("batch = %+v", batch)
	}
	if batch.Allocated > 1.0 {
		t.Fatalf("allocated %v > parent remainder", batch.Allocated)
	}
	if peak.Load() > 2 {
		t.Fatalf("concurrency peak = %d > 2", peak.Load())
	}
	for _, r := range batch.Reports {
		if r.Status != delegate.StatusCompleted {
			t.Fatalf("report = %+v", r)
		}
	}
}

// TestDelegateBudgetNeverOversubscribes is the negative control: with a
// remainder smaller than one task, the first slice is capped and every later
// task stops budget_exhausted, so the child spend cannot exceed the parent.
func TestDelegateBudgetNeverOversubscribes(t *testing.T) {
	t.Parallel()
	led := &memLedger{}
	agents := func(context.Context, *delegate.Request) (delegate.Agent, error) {
		return fakeAgent{run: func(context.Context, *delegate.Request) (delegate.AgentResult, error) {
			return delegate.AgentResult{Usage: delegate.Usage{CostMicros: 10_000_000, Calls: 1}}, nil
		}}, nil
	}
	svc, err := delegate.New(&delegate.Config{MinCallPeriod: time.Nanosecond}, agents, led, fixedBudget(0.05), nil)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := svc.Run(context.Background(), tools.Env{Session: view(t, 0)}, []delegate.Task{
		{Goal: "a", BudgetUSD: 1.0}, {Goal: "b", BudgetUSD: 1.0},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Allocated > 0.05 {
		t.Fatalf("allocated %v, remaining 0.05", batch.Allocated)
	}
	if batch.Reports[1].Status != delegate.StatusBudgetExceeded {
		t.Fatalf("second report = %+v", batch.Reports[1])
	}
	if _, calls := led.total("P"); calls != 1 {
		t.Fatalf("child calls = %d, want only the affordable one", calls)
	}
}

func TestDelegateDepthLimit(t *testing.T) {
	t.Parallel()
	led := &memLedger{}
	var gotDepth atomic.Int64
	var gotTools atomic.Value
	agents := func(_ context.Context, req *delegate.Request) (delegate.Agent, error) {
		gotDepth.Store(int64(req.Depth))
		gotTools.Store(req.View.Tools())
		return fakeAgent{run: func(context.Context, *delegate.Request) (delegate.AgentResult, error) {
			return delegate.AgentResult{StopReason: "end_turn"}, nil
		}}, nil
	}
	svc, err := delegate.New(&delegate.Config{MaxDepth: 2, MinCallPeriod: time.Nanosecond}, agents, led, fixedBudget(1), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Run(context.Background(), tools.Env{Session: view(t, 2)}, []delegate.Task{{Goal: "x"}}, 1); !errors.Is(err, delegate.ErrDepth) {
		t.Fatalf("depth 2 = %v, want ErrDepth", err)
	}
	if _, err := svc.Run(context.Background(), tools.Env{Session: view(t, 1)}, []delegate.Task{{Goal: "x"}}, 1); err != nil {
		t.Fatal(err)
	}
	if gotDepth.Load() != 2 {
		t.Fatalf("child depth = %d, want 2", gotDepth.Load())
	}
	names, ok := gotTools.Load().([]string)
	if !ok {
		t.Fatal("agent did not record its tool set")
	}
	for _, name := range names {
		if name == "delegate_task" {
			t.Fatal("deepest child still sees delegate_task")
		}
	}
}

func TestDelegateConcurrencyCap(t *testing.T) {
	t.Parallel()
	led := &memLedger{}
	var live, peak atomic.Int64
	release := make(chan struct{})
	agents := func(context.Context, *delegate.Request) (delegate.Agent, error) {
		return fakeAgent{run: func(context.Context, *delegate.Request) (delegate.AgentResult, error) {
			n := live.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			<-release
			live.Add(-1)
			return delegate.AgentResult{StopReason: "end_turn"}, nil
		}}, nil
	}
	svc, err := delegate.New(&delegate.Config{MaxParallel: 2, MinCallPeriod: time.Nanosecond}, agents, led, fixedBudget(1), nil)
	if err != nil {
		t.Fatal(err)
	}
	tasks := []delegate.Task{{Goal: "a"}, {Goal: "b"}, {Goal: "c"}, {Goal: "d"}}
	done := make(chan struct{})
	go func() {
		if _, err := svc.Run(context.Background(), tools.Env{Session: view(t, 0)}, tasks, 16); err != nil {
			t.Error(err)
		}
		close(done)
	}()
	deadline := time.After(5 * time.Second)
	for peak.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("tasks never reached the cap")
		case <-time.After(time.Millisecond):
		}
	}
	close(release)
	<-done
	if peak.Load() != 2 {
		t.Fatalf("peak = %d, want the 2-slot cap", peak.Load())
	}
}

func TestDelegateRateLimitNotice(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	notices := &fakeNotices{}
	svc, err := delegate.New(&delegate.Config{
		MinCallPeriod: 15 * time.Second, Clock: clock.Now,
	}, func(context.Context, *delegate.Request) (delegate.Agent, error) {
		return fakeAgent{run: func(context.Context, *delegate.Request) (delegate.AgentResult, error) {
			return delegate.AgentResult{StopReason: "end_turn"}, nil
		}}, nil
	}, &memLedger{}, fixedBudget(10), notices)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	env := tools.Env{Session: view(t, 0)}
	allowed := 0
	for range 100 {
		_, err := svc.Run(ctx, env, []delegate.Task{{Goal: "x"}}, 1)
		switch {
		case err == nil:
			allowed++
		case errors.Is(err, delegate.ErrRateLimited):
			// no spend
		default:
			t.Fatalf("run = %v", err)
		}
		clock.Advance(600 * time.Millisecond) // one minute across the loop
	}
	if allowed != 4 {
		t.Fatalf("allowed = %d in one minute, want 4 (one per 15 s)", allowed)
	}
	if notices.count() > 4 {
		t.Fatalf("notices = %d in one minute, want ≤ 4", notices.count())
	}
	if notices.count() == 0 {
		t.Fatal("rate limit produced no notice (negative control)")
	}
}

// approvalFixture builds a dispatcher over the delegate tool.
type approvalFixture struct {
	disp   *tools.Dispatcher
	svc    *delegate.Service
	ledger *memLedger
}

type fakeJournal struct{}

func (fakeJournal) ToolDispatch(context.Context, string, string, string, tools.Effect, [32]byte) error {
	return nil
}

func (fakeJournal) ToolResult(context.Context, string, string, string, bool, string, string) error {
	return nil
}

type fakeSpill struct{}

func (fakeSpill) Put(context.Context, []byte) (tools.BlobRef, error) { return tools.BlobRef{}, nil }
func (fakeSpill) Get(context.Context, string) ([]byte, error)        { return nil, nil }

func newApprovalFixture(t *testing.T, appr tools.Approver, view tools.SessionView) *approvalFixture {
	t.Helper()
	led := &memLedger{}
	svc, err := delegate.New(&delegate.Config{MinCallPeriod: time.Nanosecond},
		func(context.Context, *delegate.Request) (delegate.Agent, error) {
			return fakeAgent{run: func(context.Context, *delegate.Request) (delegate.AgentResult, error) {
				return delegate.AgentResult{Answer: "done", StopReason: "end_turn"}, nil
			}}, nil
		}, led, fixedBudget(1), nil)
	if err != nil {
		t.Fatal(err)
	}
	tool, err := delegate.NewTool(svc)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tools.NewRegistry(tool)
	if err != nil {
		t.Fatal(err)
	}
	disp, err := tools.NewDispatcher(&tools.Config{
		Registry: reg, Journal: fakeJournal{}, Spill: fakeSpill{},
		Redactor: redact.New(redact.Options{}), Mode: tools.ModeAsk, Cwd: "/w", Session: view,
		Approver: appr,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &approvalFixture{disp: disp, svc: svc, ledger: led}
}

func delegateArgs() json.RawMessage {
	return json.RawMessage(`{"tasks":[{"goal":"fix the failing test","role":"coder","files":["a.go"],"budget_usd":0.25}],"max_parallel":1}`)
}

//nolint:gocognit // the four approval outcomes are one matrix by design.
func TestDelegateApproval(t *testing.T) {
	t.Parallel()
	t.Run("deny", func(t *testing.T) {
		f := newApprovalFixture(t, denyApprover{}, view(t, 0))
		out := f.disp.Dispatch(context.Background(), tools.Call{Name: "delegate_task", Args: delegateArgs()})
		if out.Err == nil || !out.Denied || out.Err.Code != tools.CodeDenied {
			t.Fatalf("deny = %+v", out.Err)
		}
		if _, calls := f.ledger.total("P"); calls != 0 {
			t.Fatal("denied delegation ran a child")
		}
	})
	t.Run("allow with preview", func(t *testing.T) {
		appr := &recApprover{answer: tools.ApprovalAllowOnce}
		f := newApprovalFixture(t, appr, view(t, 0))
		out := f.disp.Dispatch(context.Background(), tools.Call{Name: "delegate_task", Args: delegateArgs()})
		if out.Err != nil {
			t.Fatalf("allow = %+v", out.Err)
		}
		if appr.req == nil {
			t.Fatal("approver never consulted")
		}
		if !strings.Contains(appr.req.Preview, "fix the failing test") || !strings.Contains(appr.req.Preview, "0.25 USD") {
			t.Fatalf("preview = %q", appr.req.Preview)
		}
		if appr.req.AllowSession {
			t.Fatal("delegate approved a session-wide allow")
		}
		if appr.req.Class != "delegate" {
			t.Fatalf("class = %q", appr.req.Class)
		}
	})
	t.Run("no approver denies", func(t *testing.T) {
		f := newApprovalFixture(t, nil, view(t, 0))
		out := f.disp.Dispatch(context.Background(), tools.Call{Name: "delegate_task", Args: delegateArgs()})
		if out.Err == nil || !out.Denied {
			t.Fatalf("nil approver = %+v", out.Err)
		}
	})
	t.Run("missing view is internal", func(t *testing.T) {
		f := newApprovalFixture(t, &recApprover{answer: tools.ApprovalAllowOnce}, tools.SessionView{})
		out := f.disp.Dispatch(context.Background(), tools.Call{Name: "delegate_task", Args: delegateArgs()})
		if out.Err == nil || out.Err.Code != tools.CodeInternal {
			t.Fatalf("missing view = %+v", out.Err)
		}
	})
}

type denyApprover struct{}

func (denyApprover) Approve(context.Context, *tools.ApprovalRequest) (tools.Approval, error) {
	return tools.ApprovalDeny, nil
}

type recApprover struct {
	answer tools.Approval
	req    *tools.ApprovalRequest
}

func (a *recApprover) Approve(_ context.Context, req *tools.ApprovalRequest) (tools.Approval, error) {
	a.req = req
	return a.answer, nil
}

// TestDelegateNoApprovalOrSecretInheritance: the child view never carries the
// parent's bypass/secret capabilities and its budget is its own slice.
func TestDelegateNoApprovalOrSecretInheritance(t *testing.T) {
	t.Parallel()
	parent, err := tools.NewSessionView("P", "", "gateway", "/w",
		[]string{"read_file", "delegate_task"},
		[]tools.Capability{tools.CapDelegate, tools.CapApprovalBypass, tools.CapSecretAccess})
	if err != nil {
		t.Fatal(err)
	}
	var got tools.SessionView
	var gotBudget float64
	svc, err := delegate.New(&delegate.Config{MinCallPeriod: time.Nanosecond},
		func(_ context.Context, req *delegate.Request) (delegate.Agent, error) {
			got, gotBudget = req.View, req.BudgetUSD
			return fakeAgent{run: func(context.Context, *delegate.Request) (delegate.AgentResult, error) {
				return delegate.AgentResult{StopReason: "end_turn"}, nil
			}}, nil
		}, &memLedger{}, fixedBudget(2), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Run(context.Background(), tools.Env{Session: parent}, []delegate.Task{{Goal: "x", BudgetUSD: 0.5}}, 1); err != nil {
		t.Fatal(err)
	}
	if got.Can(tools.CapApprovalBypass) || got.Can(tools.CapSecretAccess) {
		t.Fatalf("child caps = %v", got.Capabilities())
	}
	if !got.Can(tools.CapDelegate) || got.Depth() != 1 {
		t.Fatalf("child view = %v depth %d", got.Capabilities(), got.Depth())
	}
	if gotBudget != 0.5 {
		t.Fatalf("child budget = %v, want the explicit slice", gotBudget)
	}
}

// TestDelegateInvalidTask is the negative control for the task contract.
func TestDelegateInvalidTask(t *testing.T) {
	t.Parallel()
	svc, err := delegate.New(&delegate.Config{MinCallPeriod: time.Nanosecond},
		func(context.Context, *delegate.Request) (delegate.Agent, error) { return fakeAgent{}, nil },
		&memLedger{}, fixedBudget(1), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []delegate.Task{
		{Goal: ""},
		{Goal: "x", MaxIterations: 41},
		{Goal: "x", Isolation: "container"},
		{Goal: "x", BudgetUSD: -1},
	} {
		if _, err := svc.Run(context.Background(), tools.Env{Session: view(t, 0)}, []delegate.Task{task}, 1); !errors.Is(err, delegate.ErrInvalidConfig) {
			t.Errorf("task %+v = %v, want ErrInvalidConfig", task, err)
		}
	}
	if _, err := svc.Run(context.Background(), tools.Env{Session: view(t, 0)}, nil, 1); !errors.Is(err, delegate.ErrInvalidConfig) {
		t.Errorf("empty tasks = %v", err)
	}
}

// TestDelegateInvalidTaskDoesNotConsumeRate: contract validation runs before
// the rate limiter, so a rejected task never spends the session's slot.
// Windows shuffle exposed the opposite order (a valid call could then be
// rate-limited by a preceding invalid one).
func TestDelegateInvalidTaskDoesNotConsumeRate(t *testing.T) {
	t.Parallel()
	svc, err := delegate.New(&delegate.Config{MinCallPeriod: time.Minute},
		func(context.Context, *delegate.Request) (delegate.Agent, error) {
			return fakeAgent{run: func(context.Context, *delegate.Request) (delegate.AgentResult, error) {
				return delegate.AgentResult{StopReason: "end_turn"}, nil
			}}, nil
		}, &memLedger{}, fixedBudget(1), nil)
	if err != nil {
		t.Fatal(err)
	}
	env := tools.Env{Session: view(t, 0)}
	if _, err := svc.Run(context.Background(), env, []delegate.Task{{Goal: "x", MaxIterations: 41}}, 1); !errors.Is(err, delegate.ErrInvalidConfig) {
		t.Fatalf("invalid task = %v, want ErrInvalidConfig", err)
	}
	if _, err := svc.Run(context.Background(), env, nil, 1); !errors.Is(err, delegate.ErrInvalidConfig) {
		t.Fatalf("empty tasks = %v, want ErrInvalidConfig", err)
	}
	if _, err := svc.Run(context.Background(), env, []delegate.Task{{Goal: "valid"}}, 1); err != nil {
		t.Fatalf("valid call after invalid ones = %v, want success", err)
	}
	// Negative control: the limiter is still active for a second valid call.
	if _, err := svc.Run(context.Background(), env, []delegate.Task{{Goal: "again"}}, 1); !errors.Is(err, delegate.ErrRateLimited) {
		t.Fatalf("second valid call = %v, want ErrRateLimited", err)
	}
}

func TestDelegateToolSchema(t *testing.T) {
	t.Parallel()
	svc, err := delegate.New(&delegate.Config{MinCallPeriod: time.Nanosecond},
		func(context.Context, *delegate.Request) (delegate.Agent, error) { return fakeAgent{}, nil },
		&memLedger{}, fixedBudget(1), nil)
	if err != nil {
		t.Fatal(err)
	}
	tool, err := delegate.NewTool(svc)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	if err := json.Unmarshal(tool.Schema(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Name != "delegate_task" || len(doc.Description) > 160 {
		t.Fatalf("doc = %+v", doc)
	}
	if _, err := delegate.NewTool(nil); err == nil {
		t.Fatal("nil service accepted")
	}
	if tool.Kind() != tools.KindExecute || tool.Effect(nil) != tools.EffectNonIdempotent {
		t.Fatalf("kind/effect = %v/%v", tool.Kind(), tool.Effect(nil))
	}
}

func TestParseArgsRejectsUnknown(t *testing.T) {
	t.Parallel()
	svc, err := delegate.New(&delegate.Config{MinCallPeriod: time.Nanosecond},
		func(context.Context, *delegate.Request) (delegate.Agent, error) { return fakeAgent{}, nil },
		&memLedger{}, fixedBudget(1), nil)
	if err != nil {
		t.Fatal(err)
	}
	tool, err := delegate.NewTool(svc)
	if err != nil {
		t.Fatal(err)
	}
	_, rerr := tool.Run(context.Background(), tools.Env{Session: view(t, 0)},
		json.RawMessage(`{"tasks":[{"goal":"x","surprise":true}]}`))
	var te *tools.ToolError
	if !errors.As(rerr, &te) || te.Code != tools.CodeInvalidArgs {
		t.Fatalf("unknown field = %v", rerr)
	}
}
