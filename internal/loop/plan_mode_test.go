package loop

import (
	"context"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// plainToolset implements only Toolset, not PlanPreparer.
type plainToolset struct{ inner *fakeToolset }

func (p plainToolset) Prepare(ctx context.Context, c ToolCall) (Prepared, error) {
	return p.inner.Prepare(ctx, c)
}

func (p plainToolset) Dispatch(ctx context.Context, in *Prepared) (ToolResult, error) {
	return p.inner.Dispatch(ctx, in)
}

// runInput runs one turn with an explicit Input and returns its updates. The
// fixture helper run() always sends a plain prompt; plan mode needs the flag.
func (f *fixture) runInput(t *testing.T, in Input, mods ...func(*Deps)) []event.Update {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps := f.deps()
	for _, mod := range mods {
		mod(&deps)
	}
	l, err := New(&f.cfg, &deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out, err := l.Prompt(ctx, "S1", in)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	var ups []event.Update
	for u := range out {
		ups = append(ups, u)
	}
	return ups
}

// countStoreKind counts persisted events of one kind.
func countStoreKind(s *fakeStore, k event.Kind) int {
	n := 0
	for _, got := range s.kinds() {
		if got == k {
			n++
		}
	}
	return n
}

// TestPrepareOptionsPlanMode: the turn's plan mode reaches the plan-aware
// toolset and is carried into every prepared call (ADR-203).
func TestPrepareOptionsPlanMode(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.tools.add("read", &toolStub{prepare: func(_ context.Context, c ToolCall) (Prepared, error) {
		return Prepared{Call: c, Effect: EffectRead}, nil
	}})
	f.provider.scripts = []streamFunc{
		callScript(FinishToolCalls, ToolCall{ID: "a", Name: "read", Arguments: `{}`}),
		textScript("done", event.Usage{}),
	}
	f.runInput(t, Input{Text: "plan", PlanMode: true})
	if len(f.tools.planOpts) != 1 || !f.tools.planOpts[0].PlanMode {
		t.Fatalf("plan opts = %+v", f.tools.planOpts)
	}
	ran := f.tools.ranCount()
	if ran != 1 {
		t.Fatalf("read ran %d times, want 1", ran)
	}
	if n := countStoreKind(f.store, event.ToolRejected); n != 0 {
		t.Fatalf("read rejected %d times in plan mode", n)
	}
}

// TestDeniedBeforeDispatch: a non-read call that the toolset did not mark
// PlanAllowed is rejected before approval and dispatch: one tool_rejected and
// one paired error tool_result, zero tool_dispatch, zero execution.
func TestDeniedBeforeDispatch(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.approver = &fakeApprover{answers: []ApprovalResult{{Decision: DecisionAllowOnce}}}
	f.tools.add("write", &toolStub{prepare: func(_ context.Context, c ToolCall) (Prepared, error) {
		return Prepared{Call: c, Effect: EffectIdempotent, Approval: ApprovalAsk, Preview: "write x"}, nil
	}})
	f.provider.scripts = []streamFunc{
		callScript(FinishToolCalls, ToolCall{ID: "a", Name: "write", Arguments: `{}`}),
		textScript("done", event.Usage{}),
	}
	ups := f.runInput(t, Input{Text: "plan", PlanMode: true})
	if got := f.tools.ranCount(); got != 0 {
		t.Fatalf("dispatched %d calls in plan mode", got)
	}
	if len(f.approver.reqs) != 0 {
		t.Fatalf("approver asked %d times for a plan-mode denial", len(f.approver.reqs))
	}
	if n := countStoreKind(f.store, event.ToolDispatch); n != 0 {
		t.Fatalf("tool_dispatch events = %d", n)
	}
	if n := countStoreKind(f.store, event.ToolRejected); n != 1 {
		t.Fatalf("tool_rejected events = %d, want 1", n)
	}
	ev, ok := f.store.find(event.ToolResult)
	if !ok {
		t.Fatal("denied call has no paired tool_result")
	}
	var p resultPayload
	payloadOf(t, &ev, &p)
	if p.Error == nil || p.Error.Code != CodeDenied {
		t.Fatalf("tool_result payload = %s", ev.Payload)
	}
	// The turn itself ends normally: a denial is a tool error, not a stop.
	if got := terminal(t, ups).Stop; got != event.StopEndTurn {
		t.Fatalf("stop = %s", got)
	}
}

// TestPlanModeFailClosed: a Toolset without PreparePlan cannot prove
// compliance, so plan mode denies even a read (ADR-203).
func TestPlanModeFailClosed(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.tools.add("read", &toolStub{prepare: func(_ context.Context, c ToolCall) (Prepared, error) {
		return Prepared{Call: c, Effect: EffectRead}, nil
	}})
	f.provider.scripts = []streamFunc{
		callScript(FinishToolCalls, ToolCall{ID: "a", Name: "read", Arguments: `{}`}),
		textScript("done", event.Usage{}),
	}
	f.runInput(t, Input{Text: "plan", PlanMode: true}, func(d *Deps) { d.Toolset = plainToolset{inner: f.tools} })
	if got := f.tools.ranCount(); got != 0 {
		t.Fatalf("non-plan-aware toolset executed %d calls", got)
	}
	if n := countStoreKind(f.store, event.ToolDispatch); n != 0 {
		t.Fatalf("tool_dispatch events = %d", n)
	}
	if n := countStoreKind(f.store, event.ToolRejected); n != 1 {
		t.Fatalf("tool_rejected events = %d, want 1", n)
	}
}

// TestPlanModeAllowedDispatch: a non-read call the plan-aware toolset marked
// PlanAllowed (exact shell_allow + non_mutating) still runs.
func TestPlanModeAllowedDispatch(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.tools.add("shell", &toolStub{prepare: func(_ context.Context, c ToolCall) (Prepared, error) {
		return Prepared{Call: c, Effect: EffectIdempotent, PlanAllowed: true}, nil
	}})
	f.provider.scripts = []streamFunc{
		callScript(FinishToolCalls, ToolCall{ID: "a", Name: "shell", Arguments: `{}`}),
		textScript("done", event.Usage{}),
	}
	f.runInput(t, Input{Text: "plan", PlanMode: true})
	if got := f.tools.ranCount(); got != 1 {
		t.Fatalf("allowed call ran %d times, want 1", got)
	}
	if n := countStoreKind(f.store, event.ToolRejected); n != 0 {
		t.Fatalf("allowed call rejected %d times", n)
	}
}

// TestPlanModeOffControl is the negative control: with plan mode off, the
// same non-read call takes the ordinary path and runs.
func TestPlanModeOffControl(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.tools.add("write", &toolStub{prepare: func(_ context.Context, c ToolCall) (Prepared, error) {
		return Prepared{Call: c, Effect: EffectIdempotent}, nil
	}})
	f.provider.scripts = []streamFunc{
		callScript(FinishToolCalls, ToolCall{ID: "a", Name: "write", Arguments: `{}`}),
		textScript("done", event.Usage{}),
	}
	f.runInput(t, Input{Text: "build"})
	if got := f.tools.ranCount(); got != 1 {
		t.Fatalf("ordinary write ran %d times, want 1", got)
	}
	if n := countStoreKind(f.store, event.ToolRejected); n != 0 {
		t.Fatalf("ordinary write rejected %d times", n)
	}
	if len(f.tools.planOpts) != 0 {
		t.Fatalf("PreparePlan called without plan mode: %+v", f.tools.planOpts)
	}
}
