package loop

import (
	"context"
	"strings"
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

// LP8-2: batch waits are bounded by KillGrace.
func TestLP82BatchWaitBounded(t *testing.T) {
	t.Parallel()
	f := newFixture()
	release := make(chan struct{})
	finished := make(chan struct{})
	f.tools.add("slow", &toolStub{dispatch: func(ctx context.Context, _ *Prepared) (ToolResult, error) {
		<-release
		close(finished)
		return ToolResult{Content: "late"}, nil
	}})
	cfg := f.cfg
	cfg.KillGrace = 20 * time.Millisecond
	withDefaults(&cfg)
	tr := &turn{cfg: cfg, deps: f.deps(), out: make(chan event.Update, 8), sessionID: "S", turnID: "T"}
	ps := []Prepared{{Call: ToolCall{ID: "a", Name: "slow", Arguments: `{}`}, Effect: EffectRead}}
	start := time.Now()
	outs := tr.dispatchBatch(context.Background(), ps)
	if d := time.Since(start); d > 80*time.Millisecond {
		t.Fatalf("dispatchBatch took %v", d)
	}
	if outs[0].res.Status != "still_running" {
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
