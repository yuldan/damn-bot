package loop

import (
	"context"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

func coreTurn(t *testing.T, f *fixture) *turn {
	t.Helper()
	cfg := f.cfg
	withDefaults(&cfg)
	return &turn{cfg: cfg, deps: f.deps(), out: make(chan event.Update, 32), sessionID: "S", turnID: "T"}
}

func TestDispatchPrimitives(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.tools.stubs["read"] = &toolStub{}
	f.tools.stubs["r2"] = &toolStub{}
	tr := coreTurn(t, f)
	ps, errs, fatal := tr.prepareBatch(context.Background(), []ToolCall{
		{ID: "a", Name: "read", Arguments: `{}`},
		{ID: "b", Name: "r2", Arguments: `{}`},
	})
	if fatal != nil || errs != nil || !tr.canParallel(ps) {
		t.Fatalf("prepare: %+v %+v %v", ps, errs, fatal)
	}
	outs := tr.dispatchBatch(context.Background(), ps)
	if len(outs) != 2 || outs[0].err != nil || outs[1].res.Content != "tool-ok" {
		t.Fatalf("outs = %+v", outs)
	}
	if err := tr.dispatchEvent(context.Background(), &ps[0]); err != nil {
		t.Fatal(err)
	}
	tr.reject(context.Background(), ps[0].Call, &ToolError{Code: CodeDenied, Message: "no"})
	if got := argsSHA(`{}`); len(got) != 64 {
		t.Errorf("argsSHA = %q", got)
	}
	res, err := tr.dispatch(context.Background(), &ps[0])
	if err != nil || res.Content != "tool-ok" {
		t.Fatalf("dispatch = %+v %v", res, err)
	}
	// Duplicate id is rejected without dispatch.
	_, errs, fatal = tr.prepareBatch(context.Background(), []ToolCall{
		{ID: "a", Name: "read", Arguments: `{}`},
		{ID: "a", Name: "read", Arguments: `{}`},
	})
	if fatal != nil || errs[1] == nil || errs[1].Code != CodeInvalidArgs {
		t.Errorf("duplicate = %+v", errs)
	}
}

func TestRenderFunctions(t *testing.T) {
	t.Parallel()
	if got := errorEnvelope(&ToolError{Code: CodeInvalidArgs, Message: "m", Hint: "h"}); !strings.Contains(got, `"code":"invalid_args"`) {
		t.Fatalf("envelope = %s", got)
	}
	if runeLen("é") != 1 || preview("abcdef", 3) != "abc" || preview("abc", 5) != "abc" {
		t.Errorf("runeLen/preview")
	}
	f := newFixture()
	tr := coreTurn(t, f)
	tr.deps.Spiller = nil
	tr.cfg.MaxInline = 4
	if got := tr.modelContent(context.Background(), &ToolResult{Content: "0123456789"}); !strings.Contains(got, CodeTooLarge) {
		t.Errorf("too_large = %q", got)
	}
	tr.cfg.MaxInline = 100
	tr.cfg.MaxInlineTurn = 2
	if got := tr.modelContent(context.Background(), &ToolResult{Content: "0123"}); !strings.Contains(got, "truncated") {
		t.Errorf("aggregate = %q", got)
	}
	sp := &fakeSpiller{ref: "blob://x"}
	tr.deps.Spiller = sp
	tr.cfg.MaxInline = 2
	if got := tr.modelContent(context.Background(), &ToolResult{Content: "0123"}); !strings.Contains(got, "spilled") {
		t.Errorf("spill = %q", got)
	}
}
