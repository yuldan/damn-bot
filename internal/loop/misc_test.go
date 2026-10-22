package loop

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/loop/event"
)

func TestNewValidation(t *testing.T) {
	t.Parallel()
	if _, err := New(nil, nil); !errors.Is(err, ErrNoProvider) {
		t.Errorf("no provider: %v", err)
	}
	deps := Deps{Provider: &fakeProvider{}}
	if _, err := New(nil, &deps); !errors.Is(err, ErrNoAssembler) {
		t.Errorf("no assembler: %v", err)
	}
	deps.Assembler = &fakeAssembler{}
	l, err := New(nil, &deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Prompt(context.Background(), "", Input{}); err == nil {
		t.Error("empty session id accepted")
	}
}

func TestDefaultTurnID(t *testing.T) {
	t.Parallel()
	a, b := newTurnID(), newTurnID()
	if a == "" || b == "" || a == b {
		t.Errorf("ids = %q %q", a, b)
	}
}

func TestErrorTypes(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("root")
	se := &StreamError{Class: ClassProvider, Err: sentinel}
	if !strings.Contains(se.Error(), "provider") || !errors.Is(se, sentinel) {
		t.Errorf("StreamError: %q", se.Error())
	}
	te := &ToolError{Code: CodeDenied, Message: "no"}
	if te.Error() != "denied: no" {
		t.Errorf("ToolError = %q", te.Error())
	}
	r := TurnResult{Stop: event.StopEndTurn, Detail: event.DetailCompleted, Usage: event.Usage{Iterations: 2}}
	if !strings.Contains(r.String(), "end_turn") {
		t.Errorf("TurnResult = %q", r.String())
	}
}

// TestApprovalEditRevalidated: edited arguments are re-prepared before use.
func TestApprovalEditRevalidated(t *testing.T) {
	t.Parallel()
	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		f := newFixture()
		f.provider.scripts = []streamFunc{callScript(FinishToolCalls,
			ToolCall{ID: "a", Name: "write", Arguments: `{"p":1}`})}
		var calls []string
		f.tools.add("write", &toolStub{
			prepare: func(_ context.Context, c ToolCall) (Prepared, error) {
				calls = append(calls, c.Arguments)
				return Prepared{Call: c, Effect: EffectIdempotent, Approval: ApprovalAsk}, nil
			},
			dispatch: func(_ context.Context, p *Prepared) (ToolResult, error) {
				if p.Call.Arguments != `{"p":2}` {
					t.Errorf("dispatched %q", p.Call.Arguments)
				}
				return ToolResult{Content: "ok"}, nil
			},
		})
		f.approver = &fakeApprover{answers: []ApprovalResult{{Decision: DecisionEdit, Args: []byte(`{"p":2}`)}}}
		f.cfg.MaxIterations = 1
		f.run(t)
		if len(calls) != 2 || calls[0] != `{"p":1}` || calls[1] != `{"p":2}` {
			t.Errorf("prepare calls = %v", calls)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		t.Parallel()
		f := newFixture()
		f.provider.scripts = []streamFunc{callScript(FinishToolCalls,
			ToolCall{ID: "a", Name: "write", Arguments: `{"p":1}`})}
		f.tools.add("write", &toolStub{prepare: func(_ context.Context, c ToolCall) (Prepared, error) {
			return Prepared{Call: c, Effect: EffectIdempotent, Approval: ApprovalAsk}, nil
		}})
		f.approver = &fakeApprover{answers: []ApprovalResult{{Decision: DecisionEdit, Args: []byte(`{oops`)}}}
		f.cfg.MaxIterations = 1
		f.run(t)
		if f.tools.ranCount() != 0 {
			t.Error("invalid edit was dispatched")
		}
	})
}

// TestNoSessionNeverCached: allow_session on a NoSession class asks every time.
func TestNoSessionNeverCached(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{
		callScript(FinishToolCalls, ToolCall{ID: "a", Name: "shell", Arguments: `{}`}),
		callScript(FinishToolCalls, ToolCall{ID: "b", Name: "shell", Arguments: `{}`}),
		textScript("done", event.Usage{}),
	}
	f.tools.add("shell", &toolStub{prepare: func(_ context.Context, c ToolCall) (Prepared, error) {
		return Prepared{Call: c, Effect: EffectNonIdempotent, Approval: ApprovalAsk, NoSession: true}, nil
	}})
	f.approver = &fakeApprover{answers: []ApprovalResult{{Decision: DecisionAllowSession}}}
	f.run(t)
	if len(f.approver.reqs) != 2 {
		t.Errorf("approver asked %d times, want 2", len(f.approver.reqs))
	}
}

// TestApproverErrorIsDenial: an approver failure never runs the call.
func TestApproverErrorIsDenial(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{callScript(FinishToolCalls,
		ToolCall{ID: "a", Name: "write", Arguments: `{}`})}
	f.tools.add("write", &toolStub{prepare: func(_ context.Context, c ToolCall) (Prepared, error) {
		return Prepared{Call: c, Effect: EffectIdempotent, Approval: ApprovalAsk}, nil
	}})
	f.approver = &fakeApprover{err: errors.New("ui gone")}
	f.cfg.MaxIterations = 1
	f.run(t)
	if f.tools.ranCount() != 0 {
		t.Error("call ran after approver error")
	}
}

// TestContextLengthReCall: a typed context_length error compacts and retries
// exactly once (ADR-078 AUD-045).
func TestContextLengthReCall(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{
		errorScript(ClassContextLength, errors.New("context too long")),
		textScript("ok", event.Usage{}),
	}
	c := &fakeCompactor{}
	deps := f.deps()
	deps.Compactor = c
	if err := runWith(t, f, &deps); err != nil {
		t.Fatal(err)
	}
	if c.calls != 1 || !c.reasons[0] || f.provider.calls != 2 {
		t.Errorf("compactor calls = %d (context_length %v), provider calls = %d", c.calls, c.reasons, f.provider.calls)
	}
}

// TestLedgerErrorStops: accounting failure is never swallowed.
func TestLedgerErrorStops(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.ledger.err = errors.New("ledger down")
	got := terminal(t, f.run(t))
	if got.Stop != event.StopError || got.Detail != event.DetailInternal {
		t.Fatalf("stop = %s/%s", got.Stop, got.Detail)
	}
}

// TestStoreErrorStops: a failed append is a terminal internal error.
func TestStoreErrorStops(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.store.err = errors.New("disk gone")
	got := terminal(t, f.run(t))
	if got.Stop != event.StopError || got.Detail != event.DetailInternal {
		t.Fatalf("stop = %s/%s", got.Stop, got.Detail)
	}
	if f.provider.calls != 0 {
		t.Error("provider called after a failed turn_start append")
	}
}

// TestTruncatedToolCallNeverRuns (SPEC §4.2): finish_reason=length with a call
// yields invalid_args and no dispatch.
func TestTruncatedToolCallNeverRuns(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{callScript(FinishLength,
		ToolCall{ID: "a", Name: "read", Arguments: `{"p":`})}
	f.tools.add("read", &toolStub{})
	f.cfg.MaxIterations = 1
	got := terminal(t, f.run(t))
	if got.Stop != event.StopMaxIterations {
		t.Fatalf("stop = %s", got.Stop)
	}
	if f.tools.ranCount() != 0 {
		t.Error("truncated call was dispatched")
	}
	ev, ok := f.store.find(event.ToolResult)
	if !ok {
		t.Fatal("no invalid_args result")
	}
	var p resultPayload
	payloadOf(t, &ev, &p)
	if p.Error == nil || p.Error.Code != CodeInvalidArgs {
		t.Errorf("payload = %+v", p)
	}
}

// TestMaxWall: the injected clock ends a runaway turn as cancelled/max_wall.
func TestMaxWall(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.cfg.MaxWall = time.Second
	n := 0
	base := time.Unix(1_700_000_000, 0)
	f.cfg.Now = func() time.Time {
		n++
		if n == 1 {
			return base
		}
		return base.Add(2 * time.Second)
	}
	got := terminal(t, f.run(t))
	if got.Stop != event.StopCancelled || got.Detail != "max_wall" {
		t.Fatalf("stop = %s/%s", got.Stop, got.Detail)
	}
}

// TestAddUsageCost: costs accumulate, unknown stays nil.
func TestAddUsageCost(t *testing.T) {
	t.Parallel()
	turn := &turn{}
	c := 0.5
	u1 := event.Usage{Input: 1, Output: 2, CostUSD: &c}
	turn.addUsage(&u1)
	if turn.usage.CostUSD == nil || *turn.usage.CostUSD != 0.5 {
		t.Fatalf("cost = %v", turn.usage.CostUSD)
	}
	u2 := event.Usage{Input: 3, Output: 4}
	turn.addUsage(&u2)
	if turn.usage.Input != 4 || turn.usage.Output != 6 || *turn.usage.CostUSD != 0.5 {
		t.Errorf("usage = %+v", turn.usage)
	}
}

// TestPrepareFatal: a non-ToolError from Prepare is a tool_error terminal.
func TestPrepareFatal(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{callScript(FinishToolCalls, ToolCall{ID: "a", Name: "x", Arguments: `{}`})}
	f.tools.add("x", &toolStub{prepare: func(context.Context, ToolCall) (Prepared, error) {
		return Prepared{}, errors.New("prepare exploded")
	}})
	if got := terminal(t, f.run(t)); got.Stop != event.StopToolError {
		t.Fatalf("stop = %s", got.Stop)
	}
}
