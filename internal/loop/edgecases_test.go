package loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestEdgeCases (EDGE-CASES-hermes.md, ADR-077/080): the EC entries owned by
// the loop in phase F1.
func TestEdgeCases(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	cases := []struct {
		id string
		fn func(t *testing.T)
	}{
		{"EC-PROV-001", ecProv001},
		{"EC-PROV-015", ecProv015},
		{"EC-TCALL-001", ecTcall001},
		{"EC-TCALL-002", ecTcall002},
		{"EC-TCALL-003", ecTcall003},
		{"EC-TCALL-005", ecTcall005},
		{"EC-TCALL-006", ecTcall006},
		{"EC-TCALL-007", ecTcall007},
		{"EC-TCALL-008", ecTcall008},
		{"EC-TCALL-009", ecTcall009},
		{"EC-TCALL-012", ecTcall012},
	}
	for _, c := range cases {
		if g.Item(t, c.id, "F1", "covered") {
			t.Run(c.id, c.fn)
		}
	}
}

// EC-PROV-001: fragmented tool call deltas form exactly one call.
func ecProv001(t *testing.T) {
	f := newFixture()
	f.provider.scripts = []streamFunc{func(_ context.Context, _ *Request, emit func(Delta)) (Reply, error) {
		for _, d := range []ToolCallDelta{
			{Index: 0, HasIndex: true, ID: "c", Name: "read_", Arguments: `{"pa`},
			{Index: 0, HasIndex: true, Name: "file", Arguments: `th":"a"}`},
		} {
			emit(Delta{ToolCalls: []ToolCallDelta{d}})
		}
		return Reply{FinishReason: FinishToolCalls}, nil
	}}
	var got ToolCall
	f.tools.add("read_file", &toolStub{prepare: func(_ context.Context, c ToolCall) (Prepared, error) {
		got = c
		return Prepared{Call: c, Effect: EffectRead}, nil
	}})
	f.cfg.MaxIterations = 1
	f.run(t)
	if got.Name != "read_file" || got.Arguments != `{"path":"a"}` || f.tools.ranCount() != 1 {
		t.Fatalf("call = %+v, ran = %d", got, f.tools.ranCount())
	}
}

// EC-PROV-015: a failed fallback attempt stays visible; the next one runs.
func ecProv015(t *testing.T) {
	f := newFixture()
	f.provider.scripts = []streamFunc{func(_ context.Context, _ *Request, _ func(Delta)) (Reply, error) {
		return Reply{Content: "ok", FinishReason: FinishStop, Model: "c/z", FallbackFrom: "a/x"}, nil
	}}
	ups := f.run(t)
	if got := terminal(t, ups); got.Stop != event.StopEndTurn {
		t.Fatalf("stop = %s", got.Stop)
	}
	if _, ok := f.store.find(event.ProviderFallback); !ok {
		t.Error("fallback was not recorded")
	}
}

// capturedCall runs one native call and returns what Prepare received.
func capturedCall(t *testing.T, name, args string) ToolCall {
	t.Helper()
	f := newFixture()
	f.provider.scripts = []streamFunc{callScript(FinishToolCalls, ToolCall{ID: "a", Name: name, Arguments: args})}
	var got ToolCall
	f.tools.add(sanitizeName(name), &toolStub{prepare: func(_ context.Context, c ToolCall) (Prepared, error) {
		got = c
		return Prepared{Call: c, Effect: EffectRead}, nil
	}})
	f.cfg.MaxIterations = 1
	f.run(t)
	return got
}

// EC-TCALL-001: an empty tool_calls slice is omitted from the assistant
// payload (a provider 400 otherwise).
func ecTcall001(t *testing.T) {
	if callPayloads(nil) != nil {
		t.Fatal("callPayloads(nil) is not nil")
	}
	b := payload(assistantPayload{FinishReason: FinishStop})
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["tool_calls"]; ok {
		t.Errorf("empty tool_calls serialised: %s", b)
	}
}

// EC-TCALL-002: a duplicate tool_call id never yields two dispatches.
func ecTcall002(t *testing.T) {
	f := newFixture()
	// An adapter can hand back already-assembled calls whose ids collide;
	// the stream assembler itself merges the same id.
	f.provider.scripts = []streamFunc{func(_ context.Context, _ *Request, _ func(Delta)) (Reply, error) {
		return Reply{FinishReason: FinishToolCalls, ToolCalls: []ToolCall{
			{ID: "dup", Name: "read", Arguments: `{"p":1}`},
			{ID: "dup", Name: "read", Arguments: `{"p":2}`},
		}}, nil
	}}
	f.tools.add("read", &toolStub{})
	f.cfg.MaxIterations = 1
	f.run(t)
	if f.tools.ranCount() != 1 {
		t.Errorf("dispatched %d, want 1", f.tools.ranCount())
	}
	ev, ok := f.store.find(event.ToolRejected)
	if !ok {
		t.Fatal("duplicate id was not rejected")
	}
	var p auditPayload
	payloadOf(t, &ev, &p)
	if p.Code != CodeInvalidArgs || !strings.Contains(p.Message, "duplicate") {
		t.Errorf("payload = %+v", p)
	}
}

// EC-TCALL-003: every requested call gets exactly one result (no orphans).
func ecTcall003(t *testing.T) {
	f := newFixture()
	f.provider.scripts = []streamFunc{callScript(FinishToolCalls,
		ToolCall{ID: "a", Name: "read", Arguments: `{"p":1}`},
		ToolCall{ID: "b", Name: "read", Arguments: `{"p":2}`})}
	f.tools.add("read", &toolStub{})
	f.cfg.MaxIterations = 1
	f.run(t)
	var results int
	for _, k := range f.store.kinds() {
		if k == event.ToolResult {
			results++
		}
	}
	if results != 2 {
		t.Errorf("tool_result events = %d, want 2", results)
	}
}

// EC-TCALL-005: tool calls written as text are never parsed or executed.
func ecTcall005(t *testing.T) {
	f := newFixture()
	f.provider.scripts = []streamFunc{textScript("I will call <tool_call name=\"rm\">now</tool_call>", event.Usage{})}
	got := terminal(t, f.run(t))
	if got.Stop != event.StopEndTurn || got.Detail != event.DetailCompleted {
		t.Fatalf("stop = %s/%s", got.Stop, got.Detail)
	}
	if f.tools.ranCount() != 0 {
		t.Errorf("text tool call was executed")
	}
}

// EC-TCALL-006: XML/attribute residue after a native name is cut off.
func ecTcall006(t *testing.T) {
	got := capturedCall(t, `session_search" parameter="x"`, `{}`)
	if got.Name != "session_search" {
		t.Errorf("name = %q", got.Name)
	}
}

// EC-TCALL-007: a warning appended after JSON stays in the same valid result.
func ecTcall007(t *testing.T) {
	f := newFixture()
	f.provider.scripts = []streamFunc{callScript(FinishToolCalls, ToolCall{ID: "a", Name: "read", Arguments: `{}`})}
	const content = "{\"x\":1}\nWarning: appended by the tool"
	f.tools.add("read", &toolStub{dispatch: func(context.Context, *Prepared) (ToolResult, error) {
		return ToolResult{Content: content}, nil
	}})
	f.cfg.MaxIterations = 1
	f.run(t)
	ev, ok := f.store.find(event.ToolResult)
	if !ok || ev.Content != content {
		t.Errorf("result content = %q", ev.Content)
	}
	if !json.Valid([]byte(strings.SplitN(content, "\n", 2)[0])) {
		t.Error("test fixture is not valid JSON")
	}
}

// EC-TCALL-008: null arguments are passed through (the tool applies defaults).
func ecTcall008(t *testing.T) {
	got := capturedCall(t, "read", "null")
	if got.Arguments != "null" {
		t.Errorf("arguments = %q, want null", got.Arguments)
	}
}

// EC-TCALL-012: whitespace-only content from a fallback is empty, not final.
func ecTcall012(t *testing.T) {
	f := newFixture()
	f.provider.scripts = []streamFunc{
		func(_ context.Context, _ *Request, _ func(Delta)) (Reply, error) {
			return Reply{Content: " ", FinishReason: FinishStop, Model: "b/y", FallbackFrom: "a/x"}, nil
		},
		textScript("answer", event.Usage{}),
	}
	got := terminal(t, f.run(t))
	if got.Stop != event.StopEndTurn || got.Detail != event.DetailCompleted {
		t.Fatalf("stop = %s/%s", got.Stop, got.Detail)
	}
	if f.provider.calls != 2 {
		t.Errorf("provider calls = %d, want 2", f.provider.calls)
	}
}

// EC-TCALL-009: a final answer that is only a fragment (here: text where the
// --answer-schema turn requires a JSON object) stops as invalid_answer. The
// rule comes from the composition root's validator; the loop only owns the
// stop (ADR-129 butir 12), so no text heuristic lives here.
func ecTcall009(t *testing.T) {
	f := newFixture()
	f.provider.scripts = []streamFunc{
		callScript(FinishToolCalls, ToolCall{ID: "a", Name: "read", Arguments: `{"p":1}`}),
		textScript("Let me", event.Usage{Input: 3, Output: 1}),
	}
	f.tools.add("read", &toolStub{})
	f.validate = func(s string) error {
		if s != `{"answer":"ok"}` {
			return errors.New("answer is not the required JSON object")
		}
		return nil
	}
	got := terminal(t, f.run(t))
	if got.Stop != event.StopEndTurn || got.Detail != event.DetailInvalidAnswer {
		t.Fatalf("stop = %s/%s, want end_turn/invalid_answer", got.Stop, got.Detail)
	}
	assistant := f.store.countKind(event.AssistantMessage)
	if assistant != 2 { // the tool-call turn and the rejected fragment stay in history
		t.Fatalf("assistant events = %d, want 2", assistant)
	}

	// Negative control: an answer the validator accepts completes the turn.
	ok := newFixture()
	ok.provider.scripts = []streamFunc{
		callScript(FinishToolCalls, ToolCall{ID: "a", Name: "read", Arguments: `{"p":1}`}),
		textScript(`{"answer":"ok"}`, event.Usage{Input: 3, Output: 1}),
	}
	ok.tools.add("read", &toolStub{})
	ok.validate = f.validate
	if end := terminal(t, ok.run(t)); end.Stop != event.StopEndTurn || end.Detail != event.DetailCompleted {
		t.Fatalf("accepted answer stop = %s/%s", end.Stop, end.Detail)
	}
}
