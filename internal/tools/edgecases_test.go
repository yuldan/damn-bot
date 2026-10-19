package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestEdgeCases covers the EDGE-CASES-hermes.md EC-TCALL entries owned by
// the dispatch pipeline (ADR-077, ADR-080). Transcript repair entries
// 009/012 live in internal/loop/provider; 004/011 are F5.
func TestEdgeCases(t *testing.T) {
	g := phase.Current(t)
	cases := []struct {
		id string
		fn func(t *testing.T)
	}{
		{"EC-TCALL-001", ecEmptyArgs},
		{"EC-TCALL-002", ecDuplicateCallID},
		{"EC-TCALL-003", ecOrphanResult},
		{"EC-TCALL-005", ecUnterminatedCall},
		{"EC-TCALL-006", ecResidualName},
		{"EC-TCALL-007", ecTextAfterJSON},
		{"EC-TCALL-008", ecNullDefaults},
	}
	for _, c := range cases {
		if g.Item(t, c.id, "F1", "covered") {
			t.Run(c.id, c.fn)
		}
	}
}

func ecEmptyArgs(t *testing.T) {
	tool := &stubTool{name: "echo", schema: echoSchema("echo"), effect: EffectRead}
	d := newStubDispatcher(t, ModeAuto, nil, tool)
	out := d.Dispatch(context.Background(), Call{Name: "echo", Args: json.RawMessage(`{}`)})
	if codeOfOutcome(out) != CodeInvalidArgs {
		t.Fatalf("empty args code = %s", codeOfOutcome(out))
	}
	if tool.CallCount() != 0 {
		t.Fatal("empty args reached the tool")
	}
}

func ecDuplicateCallID(t *testing.T) {
	tool := &stubTool{name: "echo", schema: echoSchema("echo"), effect: EffectRead}
	d := newStubDispatcher(t, ModeAuto, nil, tool)
	call := Call{TurnID: "t", CallID: "same", Name: "echo", Args: json.RawMessage(`{"body":"x"}`)}
	if out := d.Dispatch(context.Background(), call); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := d.Dispatch(context.Background(), call); codeOfOutcome(out) != CodeIdempotencyConflict {
		t.Fatalf("duplicate code = %s", codeOfOutcome(out))
	}
	if tool.CallCount() != 1 {
		t.Fatalf("duplicate call executed %d times", tool.CallCount())
	}
}

func ecOrphanResult(t *testing.T) {
	tool := &stubTool{name: "fail", schema: echoSchema("fail"), effect: EffectRead,
		run: func(context.Context, Env, json.RawMessage) (Result, error) {
			return Result{}, &ToolError{Code: CodeNoMatch, Message: "nothing"}
		}}
	j := &journalRec{}
	d, err := NewDispatcher(Config{Registry: mustReg(t, tool), Journal: j, Spill: newMemSpill(), Mode: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	out := d.Dispatch(context.Background(), Call{TurnID: "t", CallID: "c1", Name: "fail", Args: json.RawMessage(`{"body":"x"}`)})
	if codeOfOutcome(out) != CodeNoMatch {
		t.Fatalf("code = %s", codeOfOutcome(out))
	}
	dc, rc := j.counts()
	if dc != 1 || rc != 1 {
		t.Fatalf("journal = %d/%d, want one call and one result (no orphan)", dc, rc)
	}
	if j.results[0].ok || j.results[0].code != "no_match" {
		t.Fatalf("result row = %+v", j.results[0])
	}
}

func ecUnterminatedCall(t *testing.T) {
	d := newStubDispatcher(t, ModeAuto, nil, &stubTool{name: "echo", schema: echoSchema("echo"), effect: EffectRead})
	out := d.Dispatch(context.Background(), Call{Name: "echo", Args: json.RawMessage(`{"body":"x"`)})
	if codeOfOutcome(out) != CodeInvalidArgs {
		t.Fatalf("code = %s", codeOfOutcome(out))
	}
}

func ecResidualName(t *testing.T) {
	d := newStubDispatcher(t, ModeAuto, nil, &stubTool{name: "echo", schema: echoSchema("echo"), effect: EffectRead})
	for _, name := range []string{`echo" parameter=x`, `echo>`, `echo `} {
		out := d.Dispatch(context.Background(), Call{Name: name, Args: json.RawMessage(`{"body":"x"}`)})
		if codeOfOutcome(out) != CodeUnknownTool {
			t.Fatalf("%q code = %s", name, codeOfOutcome(out))
		}
	}
}

func ecTextAfterJSON(t *testing.T) {
	d := newStubDispatcher(t, ModeAuto, nil, &stubTool{name: "echo", schema: echoSchema("echo"), effect: EffectRead})
	out := d.Dispatch(context.Background(), Call{Name: "echo", Args: json.RawMessage(`{"body":"x"} warning: x`)})
	if codeOfOutcome(out) != CodeInvalidArgs {
		t.Fatalf("code = %s", codeOfOutcome(out))
	}
	if strings.Contains(out.Render(), "warning:") {
		t.Fatalf("stray text leaked into the envelope: %s", out.Render())
	}
}

func ecNullDefaults(t *testing.T) {
	w := newWorkspace(t)
	w.write("a.txt", "alpha\n")
	d, _, _ := w.dispatcher(t, ModeAuto, nil)
	out := d.Dispatch(context.Background(), callOf("read_file", `{"path":"a.txt","offset":null}`))
	if out.Err != nil {
		t.Fatalf("null optional offset: %v", out.Err)
	}
	if !strings.Contains(out.Content, "alpha") {
		t.Fatalf("content = %q", out.Content)
	}
}
