package loop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// stopRow is one terminal-state scenario.
type stopRow struct {
	name   string
	cfg    func(*Config)
	script func(f *fixture) streamFunc
	edit   func(f *fixture)
	want   event.StopReason
	detail event.StopDetail
}

// TestStopReasonMatrix is the table of every terminal state (SPEC §3.5,
// ADR-110). Each row drives a real turn with fakes only.
func TestStopReasonMatrix(t *testing.T) {
	t.Parallel()
	for _, r := range stopRows() {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			runStopRow(t, r)
		})
	}
}

func stopRows() []stopRow {
	return append(stopRowsCall(), stopRowsDispatch()...)
}

func stopRowsCall() []stopRow {
	return []stopRow{
		{
			name: "end_turn",
			want: event.StopEndTurn, detail: event.DetailCompleted,
		},
		{
			name: "max_iterations",
			cfg:  func(c *Config) { c.MaxIterations = 1 },
			script: func(_ *fixture) streamFunc {
				return callScript(FinishToolCalls, ToolCall{ID: "a", Name: "read", Arguments: `{"p":1}`})
			},
			want: event.StopMaxIterations, detail: "max_iterations",
		},
		{
			name: "max_tokens_output",
			cfg:  func(c *Config) { c.MaxOutputTokens = 1 },
			script: func(_ *fixture) streamFunc {
				return textScript("long", event.Usage{Output: 10})
			},
			want: event.StopMaxTokens, detail: event.DetailBudgetOutput,
		},
		{
			name: "max_tokens_input",
			cfg:  func(c *Config) { c.MaxInputTokens = 5 },
			edit: func(f *fixture) { f.asm.prompt.InputTokens = 10 },
			want: event.StopMaxTokens, detail: event.DetailBudgetInput,
		},
		{
			name: "cancelled",
			script: func(f *fixture) streamFunc {
				return func(ctx context.Context, _ *Request, _ func(Delta)) (Reply, error) {
					f.cancel()
					<-ctx.Done()
					return Reply{}, &StreamError{Class: ClassCancelled, Err: ctx.Err()}
				}
			},
			want: event.StopCancelled, detail: event.DetailCancelled,
		},
		{
			name: "cancelled_confirmed",
			script: func(_ *fixture) streamFunc {
				return callScript(FinishToolCalls, ToolCall{ID: "a", Name: "work", Arguments: `{}`})
			},
			edit: func(f *fixture) {
				f.tools.add("work", &toolStub{dispatch: func(ctx context.Context, _ *Prepared) (ToolResult, error) {
					f.cancel()
					<-ctx.Done()
					return ToolResult{Content: "stopped", Status: "cancelled_confirmed"}, nil
				}})
			},
			want: event.StopCancelledConfirmed, detail: event.DetailCancelled,
		},
		{
			name: "cancelled_still_running",
			script: func(_ *fixture) streamFunc {
				return callScript(FinishToolCalls, ToolCall{ID: "a", Name: "work", Arguments: `{}`})
			},
			edit: func(f *fixture) {
				f.tools.add("work", &toolStub{dispatch: func(ctx context.Context, _ *Prepared) (ToolResult, error) {
					f.cancel()
					<-ctx.Done()
					return ToolResult{Status: "still_running"}, nil
				}})
			},
			want: event.StopCancelled, detail: event.DetailCancelled,
		},
	}
}

func stopRowsDispatch() []stopRow {
	return []stopRow{
		{
			name: "content_filtered",
			script: func(_ *fixture) streamFunc {
				return func(_ context.Context, _ *Request, emit func(Delta)) (Reply, error) {
					emit(Delta{Text: "partial"})
					return Reply{
						Content: "partial", FinishReason: FinishContentFilter,
						ToolCalls: []ToolCall{{ID: "a", Name: "read", Arguments: `{}`}},
					}, nil
				}
			},
			want: event.StopContentFiltered, detail: event.DetailContentFiltered,
		},
		{
			name: "error",
			script: func(_ *fixture) streamFunc {
				return errorScript(ClassProvider, errors.New("boom sk-secret"))
			},
			edit: func(f *fixture) { f.redactor = fakeRedactor{} },
			want: event.StopError, detail: event.DetailProviderError,
		},
		{
			name: "approval_denied",
			script: func(_ *fixture) streamFunc {
				return callScript(FinishToolCalls, ToolCall{ID: "a", Name: "rm", Arguments: `{}`})
			},
			edit: func(f *fixture) {
				f.tools.add("rm", &toolStub{prepare: func(_ context.Context, c ToolCall) (Prepared, error) {
					return Prepared{Call: c, Effect: EffectNonIdempotent, Approval: ApprovalAsk, Terminal: true}, nil
				}})
				f.approver = &fakeApprover{answers: []ApprovalResult{{Decision: DecisionDeny}}}
			},
			want: event.StopApprovalDenied, detail: event.DetailApprovalDenied,
		},
		{
			name: "tool_error",
			script: func(_ *fixture) streamFunc {
				return callScript(FinishToolCalls, ToolCall{ID: "a", Name: "broken", Arguments: `{}`})
			},
			edit: func(f *fixture) {
				f.tools.add("broken", &toolStub{dispatch: func(context.Context, *Prepared) (ToolResult, error) {
					return ToolResult{}, errors.New("dispatch exploded")
				}})
			},
			want: event.StopToolError, detail: event.DetailToolError,
		},
	}
}

func runStopRow(t *testing.T, r stopRow) {
	t.Helper()
	f := newFixture()
	if r.cfg != nil {
		r.cfg(&f.cfg)
	}
	if r.script != nil {
		f.provider.scripts = []streamFunc{r.script(f)}
	}
	f.tools.add("read", &toolStub{})
	if r.edit != nil {
		r.edit(f)
	}
	got := terminal(t, f.run(t))
	if got.Stop != r.want || got.Detail != r.detail {
		t.Fatalf("stop = %s/%s, want %s/%s", got.Stop, got.Detail, r.want, r.detail)
	}
	if r.name == "content_filtered" && f.tools.ranCount() != 0 {
		t.Errorf("content-filtered reply executed %d tool calls", f.tools.ranCount())
	}
	if r.name == "approval_denied" && f.tools.ranCount() != 0 {
		t.Errorf("denied call was dispatched")
	}
	if r.name == "error" && strings.Contains(got.Err, "sk-secret") {
		t.Errorf("error value not redacted: %q", got.Err)
	}
}

// TestContentFilterStop (ADR-072/082): partial content and any tool calls are
// stored, nothing executes, and the stop reason is content_filtered.
func TestContentFilterStop(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{func(_ context.Context, _ *Request, emit func(Delta)) (Reply, error) {
		emit(Delta{Text: "partial"})
		return Reply{
			Content: "partial", FinishReason: FinishContentFilter,
			ToolCalls: []ToolCall{{ID: "c1", Name: "read", Arguments: `{}`}},
		}, nil
	}}
	f.tools.add("read", &toolStub{})
	terminalUp := terminal(t, f.run(t))
	if terminalUp.Stop != event.StopContentFiltered {
		t.Fatalf("stop = %s", terminalUp.Stop)
	}
	ev, ok := f.store.find(event.AssistantMessage)
	if !ok {
		t.Fatal("no assistant_message event")
	}
	var p assistantPayload
	payloadOf(t, &ev, &p)
	if !p.Filtered || ev.Content != "partial" || len(p.ToolCalls) != 1 {
		t.Errorf("filtered event = %+v content %q", p, ev.Content)
	}
}

// TestFallbackVisible (P7): a fallback is never silent.
func TestFallbackVisible(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{func(_ context.Context, _ *Request, _ func(Delta)) (Reply, error) {
		return Reply{Content: "from secondary", FinishReason: FinishStop, Model: "b/y", FallbackFrom: "a/x"}, nil
	}}
	ups := f.run(t)
	if got := terminal(t, ups); got.Stop != event.StopEndTurn {
		t.Fatalf("stop = %s", got.Stop)
	}
	found := false
	for _, u := range ups {
		if u.Kind == event.UpdateNotice && strings.Contains(u.Text, "a/x -> b/y") {
			found = true
		}
	}
	if !found {
		t.Errorf("no visible fallback update: %+v", ups)
	}
	ev, ok := f.store.find(event.ProviderFallback)
	if !ok {
		t.Fatal("no provider_fallback event")
	}
	var p auditPayload
	payloadOf(t, &ev, &p)
	if p.From != "a/x" || p.To != "b/y" {
		t.Errorf("fallback payload = %+v", p)
	}
}

// TestEmptyResponseRetry (COST-15): exactly one re-call without a nudge.
func TestEmptyResponseRetry(t *testing.T) {
	t.Parallel()
	f := newFixture()
	empty := func(context.Context, *Request, func(Delta)) (Reply, error) {
		return Reply{FinishReason: FinishStop, Model: "fake/m"}, nil
	}
	f.provider.scripts = []streamFunc{empty, empty, textScript("answer", event.Usage{})}
	ups := f.run(t)
	if got := terminal(t, ups); got.Stop != event.StopEndTurn || got.Detail != event.DetailEmptyResponse {
		t.Fatalf("stop = %s/%s", got.Stop, got.Detail)
	}
	if f.provider.calls != 2 {
		t.Errorf("provider calls = %d, want 2 (one retry, then stop)", f.provider.calls)
	}
}

// TestContextGuard: one compaction at 75 %, hard stop at 90 %.
func TestContextGuard(t *testing.T) {
	t.Parallel()
	t.Run("compacts_once", func(t *testing.T) {
		t.Parallel()
		f := newFixture()
		f.asm.prompt.InputTokens, f.asm.prompt.WindowTokens = 80, 100
		c := &fakeCompactor{}
		deps := f.deps()
		deps.Compactor = c
		f.cfg.MaxIterations = 1
		f.provider.scripts = []streamFunc{textScript("ok", event.Usage{})}
		if err := runWith(t, f, &deps); err != nil {
			t.Fatal(err)
		}
		if c.calls != 1 || c.reasons[0] {
			t.Errorf("compactor calls = %d (context_length %v), want 1 threshold call", c.calls, c.reasons)
		}
	})
	t.Run("overflow", func(t *testing.T) {
		t.Parallel()
		f := newFixture()
		f.asm.prompt.InputTokens, f.asm.prompt.WindowTokens = 95, 100
		got := terminal(t, f.run(t))
		if got.Stop != event.StopMaxTokens || got.Detail != event.DetailContextOverflow {
			t.Fatalf("stop = %s/%s", got.Stop, got.Detail)
		}
		if f.provider.calls != 0 {
			t.Errorf("provider called on an over-window prompt")
		}
	})
}

// fakeCompactor counts calls and records the contextLength trigger of each.
type fakeCompactor struct {
	calls   int
	reasons []bool
}

func (c *fakeCompactor) Compact(_ context.Context, _ string, contextLength bool) error {
	c.calls++
	c.reasons = append(c.reasons, contextLength)
	return nil
}

// TestNoProgress (COST-07): the second identical (tool, args, result) ends the
// turn with end_turn/no_progress.
func TestNoProgress(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.cfg.MaxIdentical = 2
	f.provider.scripts = []streamFunc{callScript(FinishToolCalls, ToolCall{ID: "a", Name: "read", Arguments: `{"p":1}`})}
	f.tools.add("read", &toolStub{})
	got := terminal(t, f.run(t))
	if got.Stop != event.StopEndTurn || got.Detail != event.DetailNoProgress {
		t.Fatalf("stop = %s/%s", got.Stop, got.Detail)
	}
}

// TestAllowSessionCachesApproval: a session grant covers the same subject.
func TestAllowSessionCachesApproval(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{
		callScript(FinishToolCalls, ToolCall{ID: "a", Name: "write", Arguments: `{"p":1}`}),
		callScript(FinishToolCalls, ToolCall{ID: "b", Name: "write", Arguments: `{"p":1}`}),
		textScript("done", event.Usage{}),
	}
	f.tools.add("write", &toolStub{prepare: func(_ context.Context, c ToolCall) (Prepared, error) {
		return Prepared{Call: c, Effect: EffectIdempotent, Approval: ApprovalAsk, Subject: "p=1"}, nil
	}})
	f.approver = &fakeApprover{answers: []ApprovalResult{{Decision: DecisionAllowSession}}}
	if got := terminal(t, f.run(t)); got.Stop != event.StopEndTurn {
		t.Fatalf("stop = %s", got.Stop)
	}
	if len(f.approver.reqs) != 1 {
		t.Errorf("approver asked %d times, want 1", len(f.approver.reqs))
	}
	if f.tools.ranCount() != 2 {
		t.Errorf("dispatched %d, want 2", f.tools.ranCount())
	}
}

// TestTaintForcesApproval (ADR-070): after untrusted output every non-read
// action asks, and allow_session is downgraded to allow_once.
func TestTaintForcesApproval(t *testing.T) {
	t.Parallel()
	f := newFixture()
	call := func(id string) ToolCall { return ToolCall{ID: id, Name: "write", Arguments: `{"p":1}`} }
	f.provider.scripts = []streamFunc{
		callScript(FinishToolCalls, call("a")),
		func(_ context.Context, _ *Request, emit func(Delta)) (Reply, error) {
			emit(Delta{ToolCalls: []ToolCallDelta{{Index: 0, HasIndex: true, ID: "b", Name: "write", Arguments: `{"p":1}`}}})
			return Reply{FinishReason: FinishToolCalls, ToolCalls: []ToolCall{call("b")}}, nil
		},
		func(_ context.Context, _ *Request, emit func(Delta)) (Reply, error) {
			emit(Delta{ToolCalls: []ToolCallDelta{{Index: 0, HasIndex: true, ID: "c", Name: "write", Arguments: `{"p":1}`}}})
			return Reply{FinishReason: FinishToolCalls, ToolCalls: []ToolCall{call("c")}}, nil
		},
		textScript("done", event.Usage{}),
	}
	f.tools.add("read", &toolStub{})
	f.tools.add("write", &toolStub{prepare: func(_ context.Context, c ToolCall) (Prepared, error) {
		return Prepared{Call: c, Effect: EffectIdempotent, Approval: ApprovalNone, Subject: "p=1"}, nil
	}})
	// The first write returns untrusted output; later writes must ask.
	f.tools.stubs["write"].dispatch = func(_ context.Context, p *Prepared) (ToolResult, error) {
		return ToolResult{Content: "untrusted", Tainted: p.Call.ID == "a"}, nil
	}
	f.approver = &fakeApprover{answers: []ApprovalResult{{Decision: DecisionAllowSession}}}
	if got := terminal(t, f.run(t)); got.Stop != event.StopEndTurn {
		t.Fatalf("stop = %s", got.Stop)
	}
	// a is approved (allow_session does not apply to a tainted non-read only
	// after the result, so it is cached for a); b and c are forced to ask
	// because taint is now sticky.
	if len(f.approver.reqs) != 2 {
		t.Fatalf("approver asked %d times, want 2 (b and c)", len(f.approver.reqs))
	}
	if !f.approver.reqs[1].Tainted {
		t.Error("later approval was not marked tainted")
	}
}

// TestTurnInProgress (SPEC §4.2): one active turn per session.
func TestTurnInProgress(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{func(ctx context.Context, _ *Request, _ func(Delta)) (Reply, error) {
		<-ctx.Done()
		return Reply{}, &StreamError{Class: ClassCancelled, Err: ctx.Err()}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	deps := f.deps()
	l, err := New(&f.cfg, &deps)
	if err != nil {
		t.Fatal(err)
	}
	out, err := l.Prompt(ctx, "S1", Input{Text: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Prompt(ctx, "S1", Input{Text: "two"}); !errors.Is(err, ErrTurnInProgress) {
		t.Errorf("second Prompt = %v, want ErrTurnInProgress", err)
	}
	cancel()
	for range out {
	}
}

// TestCancelAPI: Cancel(session) cancels the running turn.
func TestCancelAPI(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{func(ctx context.Context, _ *Request, _ func(Delta)) (Reply, error) {
		f.loopRef.Cancel("S1")
		<-ctx.Done()
		return Reply{}, &StreamError{Class: ClassCancelled, Err: ctx.Err()}
	}}
	if got := terminal(t, f.run(t)); got.Stop != event.StopCancelled {
		t.Fatalf("stop = %s", got.Stop)
	}
}

// runWith builds a loop with an explicit Deps.
func runWith(t *testing.T, f *fixture, deps *Deps) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.cancel = cancel
	l, err := New(&f.cfg, deps)
	if err != nil {
		return err
	}
	f.loopRef = l
	out, err := l.Prompt(ctx, "S1", Input{Text: "hello"})
	if err != nil {
		return err
	}
	for range out {
	}
	return nil
}

// TestHistoryIncludesToolResults: iteration 2 sees the assistant tool-call
// message and the tool result produced by iteration 1.
func TestHistoryIncludesToolResults(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{
		callScript(FinishToolCalls, ToolCall{ID: "a", Name: "read", Arguments: `{}`}),
		textScript("done", event.Usage{}),
	}
	f.tools.add("read", &toolStub{dispatch: func(context.Context, *Prepared) (ToolResult, error) {
		return ToolResult{Content: "content-1"}, nil
	}})
	if got := terminal(t, f.run(t)); got.Stop != event.StopEndTurn {
		t.Fatalf("stop = %s", got.Stop)
	}
	f.asm.mu.Lock()
	hs := f.asm.histories
	f.asm.mu.Unlock()
	if len(hs) < 2 {
		t.Fatalf("assembler histories = %d", len(hs))
	}
	var tool, assistant bool
	for _, m := range hs[1] {
		if m.Role == "tool" && m.ToolCallID == "a" && m.Content == "content-1" {
			tool = true
		}
		if m.Role == "assistant" && len(m.ToolCalls) == 1 && m.ToolCalls[0].Name == "read" {
			assistant = true
		}
	}
	if !tool || !assistant {
		t.Fatalf("second history = %+v", hs[1])
	}
}

// TestLedgerGetsEventID: usage is anchored to the llm_call event just appended.
func TestLedgerGetsEventID(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.run(t)
	f.ledger.mu.Lock()
	ids := append([]int64(nil), f.ledger.eventIDs...)
	f.ledger.mu.Unlock()
	if len(ids) != 1 || ids[0] == 0 {
		t.Fatalf("ledger event ids = %v", ids)
	}
}
