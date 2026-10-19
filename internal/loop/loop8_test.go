package loop

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// LP8-1: a completed result is persisted before a cancellation stop.
func TestLP81CancelPersistsResult(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{callScript(FinishToolCalls, ToolCall{ID: "a", Name: "work", Arguments: `{}`})}
	f.tools.add("work", &toolStub{dispatch: func(ctx context.Context, _ *Prepared) (ToolResult, error) {
		f.cancel()
		return ToolResult{Content: "side-effect", Status: "cancelled_confirmed"}, nil
	}})
	got := terminal(t, f.run(t))
	if got.Stop != event.StopCancelledConfirmed {
		t.Fatalf("stop = %s", got.Stop)
	}
	ev, ok := f.store.find(event.ToolResult)
	if !ok || ev.Content != "side-effect" {
		t.Fatalf("tool_result missing: %+v", ev)
	}
}

// LP8-1b: a completed ("exited") result is persisted and the stop is plain cancelled.
func TestLP81CompletedResultNotConfirmed(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{callScript(FinishToolCalls, ToolCall{ID: "a", Name: "work", Arguments: `{}`})}
	f.tools.add("work", &toolStub{dispatch: func(ctx context.Context, _ *Prepared) (ToolResult, error) {
		f.cancel()
		return ToolResult{Content: "done", Status: "exited"}, nil
	}})
	got := terminal(t, f.run(t))
	if got.Stop != event.StopCancelled {
		t.Fatalf("stop = %s", got.Stop)
	}
	if _, ok := f.store.find(event.ToolResult); !ok {
		t.Fatal("completed result dropped")
	}
}

// LP8-2/LP8-18: a canceled batch wait is bounded by KillGrace.
func TestLP82BatchWaitBounded(t *testing.T) {
	t.Parallel()
	f := newFixture()
	release := make(chan struct{})
	finished := make(chan struct{})
	f.tools.add("slow", &toolStub{dispatch: func(context.Context, *Prepared) (ToolResult, error) {
		<-release
		close(finished)
		return ToolResult{Content: "late"}, nil
	}})
	cfg := f.cfg
	cfg.KillGrace = 20 * time.Millisecond
	withDefaults(&cfg)
	tr := &turn{cfg: cfg, deps: f.deps(), out: make(chan event.Update, 8), sessionID: "S", turnID: "T"}
	ps := []Prepared{{Call: ToolCall{ID: "a", Name: "slow", Arguments: `{}`}, Effect: EffectRead}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-time.After(5 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	outs := tr.dispatchBatch(ctx, ps)
	if d := time.Since(start); d > 80*time.Millisecond {
		t.Fatalf("dispatchBatch took %v", d)
	}
	if outs[0].res.Status != statusStillRunning {
		t.Fatalf("status = %q", outs[0].res.Status)
	}
	close(release)
	<-finished
}

// LP8-3: an empty subject is never cached.
func TestLP83EmptySubjectNotCached(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{
		callScript(FinishToolCalls, ToolCall{ID: "a", Name: "x", Arguments: `{}`}),
		callScript(FinishToolCalls, ToolCall{ID: "b", Name: "x", Arguments: `{}`}),
		textScript("done", event.Usage{}),
	}
	f.tools.add("x", &toolStub{prepare: func(_ context.Context, c ToolCall) (Prepared, error) {
		return Prepared{Call: c, Effect: EffectIdempotent, Approval: ApprovalAsk, Subject: ""}, nil
	}})
	f.approver = &fakeApprover{answers: []ApprovalResult{{Decision: DecisionAllowSession}}}
	f.run(t)
	if len(f.approver.reqs) != 2 {
		t.Fatalf("approver asked %d", len(f.approver.reqs))
	}
}

// LP8-4: approval marks the Prepared for DispatchPrepared.
func TestLP84ApprovedSet(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{callScript(FinishToolCalls, ToolCall{ID: "a", Name: "x", Arguments: `{}`})}
	f.tools.add("x", &toolStub{
		prepare: func(_ context.Context, c ToolCall) (Prepared, error) {
			return Prepared{Call: c, Effect: EffectIdempotent, Approval: ApprovalAsk}, nil
		},
		dispatch: func(_ context.Context, p *Prepared) (ToolResult, error) {
			if !p.Approved {
				t.Error("Prepared.Approved not set")
			}
			return ToolResult{Content: "ok"}, nil
		},
	})
	f.approver = &fakeApprover{answers: []ApprovalResult{{Decision: DecisionAllowOnce}}}
	f.cfg.MaxIterations = 1
	f.run(t)
}

// LP8-5: no_progress does not drop later results in the batch.
func TestLP85NoProgressKeepsResults(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.cfg.MaxIdentical = 1
	f.provider.scripts = []streamFunc{callScript(FinishToolCalls,
		ToolCall{ID: "a", Name: "read", Arguments: `{}`},
		ToolCall{ID: "b", Name: "read", Arguments: `{}`})}
	f.tools.add("read", &toolStub{})
	got := terminal(t, f.run(t))
	var n int
	for _, k := range f.store.kinds() {
		if k == event.ToolResult {
			n++
		}
	}
	if n != 2 || got.Detail != event.DetailNoProgress {
		t.Fatalf("results=%d stop=%s/%s", n, got.Stop, got.Detail)
	}
}

// LP8-6: stream and assistant content are redacted.
func TestLP86RedactsStreamAndAssistant(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.redactor = fakeRedactor{}
	f.provider.scripts = []streamFunc{textScript("token sk-secret", event.Usage{})}
	ups := f.run(t)
	for i := range ups {
		if strings.Contains(ups[i].Text, "sk-secret") {
			t.Fatalf("update leaked: %+v", ups[i])
		}
	}
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	for _, e := range f.store.events {
		if strings.Contains(e.Content, "sk-secret") || strings.Contains(string(e.Payload), "sk-secret") {
			t.Fatalf("event leaked: %+v", e)
		}
	}
}

// LP8-6b: tool-call arguments are redacted in stream updates and payloads.
func TestLP86RedactsToolArgs(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.redactor = fakeRedactor{}
	f.provider.scripts = []streamFunc{callScript(FinishToolCalls, ToolCall{ID: "a", Name: "read", Arguments: `{"token":"sk-secret"}`})}
	f.tools.add("read", &toolStub{})
	f.cfg.MaxIterations = 1
	ups := f.run(t)
	for i := range ups {
		if strings.Contains(ups[i].Text, "sk-secret") {
			t.Fatalf("update leaked: %+v", ups[i])
		}
	}
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	for _, e := range f.store.events {
		if strings.Contains(string(e.Payload), "sk-secret") {
			t.Fatalf("payload leaked: %+v", e)
		}
	}
}

// LP8-7: spill previews consume the turn inline budget.
func TestLP87PreviewCountsInline(t *testing.T) {
	t.Parallel()
	cfg := Config{MaxInline: 2, MaxInlineTurn: 5}
	withDefaults(&cfg)
	tr := &turn{cfg: cfg, deps: Deps{Spiller: &fakeSpiller{ref: "blob://x"}}, out: make(chan event.Update, 8)}
	_ = tr.modelContent(context.Background(), &ToolResult{Content: "0123456789"})
	first := tr.inline
	if first == 0 {
		t.Fatal("preview not counted")
	}
	got := tr.modelContent(context.Background(), &ToolResult{Content: "0123456789"})
	if !strings.Contains(got, "truncated") && !strings.Contains(got, "spilled") {
		t.Fatalf("second result bypassed budget: %q", got)
	}

	// No Spiller + small aggregate budget must still count truncation.
	cfg2 := Config{MaxInline: 100, MaxInlineTurn: 5}
	withDefaults(&cfg2)
	tr2 := &turn{cfg: cfg2, deps: Deps{}, out: make(chan event.Update, 8)}
	_ = tr2.modelContent(context.Background(), &ToolResult{Content: "0123456789"})
	if tr2.inline == 0 {
		t.Fatal("truncation not counted")
	}
	got2 := tr2.modelContent(context.Background(), &ToolResult{Content: "0123456789"})
	if !strings.Contains(got2, "truncated") || !strings.Contains(got2, "[truncated: turn inline budget]") {
		t.Fatalf("second result bypassed budget: %q", got2)
	}
}

// LP8-8: SPEC details exist as constants.
func TestLP88Details(t *testing.T) {
	t.Parallel()
	if event.DetailMaxWall != "max_wall" || event.DetailMaxIterations != "max_iterations" {
		t.Fatal("detail constants")
	}
}

// countToolResults reports how many tool_result events the store holds.
func countToolResults(f *fixture) int {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	n := 0
	for i := range f.store.events {
		if f.store.events[i].Kind == event.ToolResult {
			n++
		}
	}
	return n
}

// toolResultStatuses returns the synthesized status of every tool_result.
func toolResultStatuses(t *testing.T, f *fixture) []string {
	t.Helper()
	f.store.mu.Lock()
	events := append([]event.Event(nil), f.store.events...)
	f.store.mu.Unlock()
	var out []string
	for i := range events {
		if events[i].Kind != event.ToolResult {
			continue
		}
		var p resultPayload
		payloadOf(t, &events[i], &p)
		out = append(out, p.Status)
	}
	return out
}

// drain collects a prompt stream.
func drain(out <-chan event.Update) []event.Update {
	var ups []event.Update
	for u := range out {
		ups = append(ups, u)
	}
	return ups
}

// LP8-16: a fatal tool_error in a parallel batch still writes the result of
// the call that already ran.
func TestLP816ParallelToolErrorKeepsResults(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{callScript(FinishToolCalls,
		ToolCall{ID: "a", Name: "read", Arguments: `{}`},
		ToolCall{ID: "b", Name: "read", Arguments: `{"n":1}`})}
	f.tools.add("read", &toolStub{dispatch: func(_ context.Context, p *Prepared) (ToolResult, error) {
		if p.Call.ID == "b" {
			return ToolResult{}, errors.New("dispatch exploded")
		}
		return ToolResult{Content: "ok-a"}, nil
	}})
	got := terminal(t, f.run(t))
	if got.Stop != event.StopToolError {
		t.Fatalf("stop = %s/%s", got.Stop, got.Detail)
	}
	if n := countToolResults(f); n != 2 {
		t.Fatalf("tool_results = %d, want 2", n)
	}
	if n := f.tools.ranCount(); n != 2 {
		t.Fatalf("ran = %d, want 2", n)
	}
}

// LP8-16: cancellation after both parallel reads finished keeps both results.
func TestLP816ParallelCancelKeepsResults(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{callScript(FinishToolCalls,
		ToolCall{ID: "a", Name: "read", Arguments: `{}`},
		ToolCall{ID: "b", Name: "read", Arguments: `{"n":1}`})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var entered atomic.Int32
	release := make(chan struct{})
	f.tools.add("read", &toolStub{dispatch: func(_ context.Context, p *Prepared) (ToolResult, error) {
		if entered.Add(1) == 2 {
			cancel()
			close(release)
		}
		<-release
		return ToolResult{Content: "done-" + p.Call.ID}, nil
	}})
	deps := f.deps()
	l, err := New(&f.cfg, &deps)
	if err != nil {
		t.Fatal(err)
	}
	out, err := l.Prompt(ctx, "S1", Input{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	got := terminal(t, drain(out))
	if got.Stop != event.StopCancelled {
		t.Fatalf("stop = %s/%s", got.Stop, got.Detail)
	}
	if n := countToolResults(f); n != 2 {
		t.Fatalf("tool_results = %d, want 2", n)
	}
	if n := f.tools.ranCount(); n != 2 {
		t.Fatalf("ran = %d, want 2", n)
	}
}

// LP8-16: a serial terminal after the first call pairs the unlaunched one.
func TestLP816SerialPairsUnlaunched(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{callScript(FinishToolCalls,
		ToolCall{ID: "a", Name: "work", Arguments: `{"n":1}`},
		ToolCall{ID: "b", Name: "work", Arguments: `{"n":2}`})}
	f.tools.add("work", &toolStub{
		prepare: func(_ context.Context, c ToolCall) (Prepared, error) {
			return Prepared{Call: c, Effect: EffectIdempotent, Approval: ApprovalNone, Subject: "p"}, nil
		},
		dispatch: func(_ context.Context, p *Prepared) (ToolResult, error) {
			if p.Call.ID == "a" {
				f.cancel()
			}
			return ToolResult{Content: "ran-" + p.Call.ID}, nil
		},
	})
	got := terminal(t, f.run(t))
	if got.Stop != event.StopCancelled {
		t.Fatalf("stop = %s/%s", got.Stop, got.Detail)
	}
	if n := f.tools.ranCount(); n != 1 {
		t.Fatalf("ran = %d, want 1", n)
	}
	statuses := toolResultStatuses(t, f)
	if len(statuses) != 2 || statuses[1] != statusNotStarted {
		t.Fatalf("statuses = %v, want [\"\" %q]", statuses, statusNotStarted)
	}
}

// LP8-17: a cached allow_session decision still sets Prepared.Approved.
func TestLP817CachedApprovalSetsApproved(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{
		callScript(FinishToolCalls, ToolCall{ID: "a", Name: "x", Arguments: `{}`}),
		callScript(FinishToolCalls, ToolCall{ID: "b", Name: "x", Arguments: `{}`}),
		textScript("done", event.Usage{}),
	}
	var approved []bool
	f.tools.add("x", &toolStub{
		prepare: func(_ context.Context, c ToolCall) (Prepared, error) {
			return Prepared{Call: c, Effect: EffectIdempotent, Approval: ApprovalAsk, Subject: "s"}, nil
		},
		dispatch: func(_ context.Context, p *Prepared) (ToolResult, error) {
			approved = append(approved, p.Approved)
			return ToolResult{Content: "ok"}, nil
		},
	})
	f.approver = &fakeApprover{answers: []ApprovalResult{{Decision: DecisionAllowSession}}}
	f.run(t)
	if len(approved) != 2 || !approved[0] || !approved[1] {
		t.Fatalf("approved = %v, want [true true]", approved)
	}
	if n := len(f.approver.reqs); n != 1 {
		t.Fatalf("approver asked %d times, want 1", n)
	}
}

// LP8-18: a slow but successful read keeps its late result; KillGrace only
// bounds a canceled turn.
func TestLP818SlowLiveReadKeepsResult(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.cfg.MaxIterations = 1
	f.cfg.KillGrace = 5 * time.Millisecond
	f.provider.scripts = []streamFunc{callScript(FinishToolCalls,
		ToolCall{ID: "a", Name: "read", Arguments: `{}`},
		ToolCall{ID: "b", Name: "read", Arguments: `{"n":1}`})}
	f.tools.add("read", &toolStub{dispatch: func(_ context.Context, p *Prepared) (ToolResult, error) {
		<-time.After(20 * time.Millisecond)
		return ToolResult{Content: "late-" + p.Call.ID}, nil
	}})
	terminal(t, f.run(t))
	if n := countToolResults(f); n != 2 {
		t.Fatalf("tool_results = %d, want 2", n)
	}
	for i, s := range toolResultStatuses(t, f) {
		if s == statusStillRunning {
			t.Fatalf("result %d was reported %q", i, s)
		}
	}
}

// LP8-19: a secret split across text deltas is redacted as one value at the
// assembly boundary.
func TestLP819SplitSecretText(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.redactor = fakeRedactor{}
	f.provider.scripts = []streamFunc{func(_ context.Context, _ *Request, emit func(Delta)) (Reply, error) {
		emit(Delta{Text: "key sk-"})
		emit(Delta{Text: "secret end"})
		return Reply{Content: "key sk-secret end", FinishReason: FinishStop}, nil
	}}
	ups := f.run(t)
	var sb strings.Builder
	for i := range ups {
		if ups[i].Kind == event.UpdateTextDelta {
			sb.WriteString(ups[i].Text)
		}
	}
	if strings.Contains(sb.String(), "sk-secret") {
		t.Fatalf("split text secret leaked: %q", sb.String())
	}
}

// LP8-19: a secret split across tool-argument deltas is redacted as one value
// at the assembly boundary.
func TestLP819SplitSecretToolArgs(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.redactor = fakeRedactor{}
	f.cfg.MaxIterations = 1
	f.provider.scripts = []streamFunc{func(_ context.Context, _ *Request, emit func(Delta)) (Reply, error) {
		emit(Delta{ToolCalls: []ToolCallDelta{
			{Index: 0, HasIndex: true, ID: "a", Name: "read", Arguments: `{"tok":"sk-`},
		}})
		emit(Delta{ToolCalls: []ToolCallDelta{
			{Index: 0, HasIndex: true, Arguments: `secret"}`},
		}})
		return Reply{FinishReason: FinishToolCalls, ToolCalls: []ToolCall{
			{ID: "a", Name: "read", Arguments: `{"tok":"sk-secret"}`},
		}}, nil
	}}
	f.tools.add("read", &toolStub{})
	ups := f.run(t)
	var sb strings.Builder
	for i := range ups {
		if ups[i].Kind == event.UpdateToolCallArgs {
			sb.WriteString(ups[i].Text)
		}
	}
	if strings.Contains(sb.String(), "sk-secret") {
		t.Fatalf("split argument secret leaked: %q", sb.String())
	}
}

// prefixHoldFilter models the cross-delta filter seam (ADR-081 butir 3): it
// emits everything except a suffix that may still become the fake secret.
type prefixHoldFilter struct{ buf strings.Builder }

func (f *prefixHoldFilter) Push(dst, chunk []byte) []byte {
	f.buf.Write(chunk)
	s := f.buf.String()
	secret := "sk-secret"
	keep := 0
	for k := min(len(s), len(secret)); k > 0; k-- {
		if strings.HasPrefix(secret, s[len(s)-k:]) {
			keep = k
			break
		}
	}
	out := s[:len(s)-keep]
	f.buf.Reset()
	f.buf.WriteString(s[len(s)-keep:])
	return append(dst, fakeRedactor{}.Redact(out)...)
}

func (f *prefixHoldFilter) Flush(dst []byte) []byte {
	out := fakeRedactor{}.Redact(f.buf.String())
	f.buf.Reset()
	return append(dst, out...)
}

// LP8-19: the configured stream filter decides the split secret per delta.
func TestLP819StreamFilterSplit(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.redactor = fakeRedactor{}
	f.stream = func() StreamFilter { return &prefixHoldFilter{} }
	f.provider.scripts = []streamFunc{func(_ context.Context, _ *Request, emit func(Delta)) (Reply, error) {
		emit(Delta{Text: "key sk-"})
		emit(Delta{Text: "secret end"})
		return Reply{Content: "key sk-secret end", FinishReason: FinishStop}, nil
	}}
	ups := f.run(t)
	var sb strings.Builder
	var updates int
	for i := range ups {
		if ups[i].Kind == event.UpdateTextDelta {
			updates++
			sb.WriteString(ups[i].Text)
		}
	}
	if strings.Contains(sb.String(), "sk-secret") {
		t.Fatalf("stream filter leaked: %q", sb.String())
	}
	if updates == 0 {
		t.Fatal("stream filter suppressed every text delta")
	}
}

// LP8-20: a batch member that never started is reported not_started, not as
// a confirmed cancellation.
func TestLP820UnstartedMemberNotConfirmed(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.tools.add("read", &toolStub{})
	cfg := f.cfg
	withDefaults(&cfg)
	tr := &turn{cfg: cfg, deps: f.deps(), out: make(chan event.Update, 8), sessionID: "S", turnID: "T"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ps := []Prepared{{Call: ToolCall{ID: "a", Name: "read", Arguments: `{}`}, Effect: EffectRead}}
	outs := tr.dispatchBatch(ctx, ps)
	if outs[0].res.Status != statusNotStarted {
		t.Fatalf("status = %q, want %q", outs[0].res.Status, statusNotStarted)
	}
	if n := f.tools.ranCount(); n != 0 {
		t.Fatalf("ran = %d, want 0", n)
	}
}
