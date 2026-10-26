package loop

import (
	"context"
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// TestApprovalOrigin: the loop routes Deps.Origin to every approval request;
// an empty value takes the headless `run` default (ADR-197).
func TestApprovalOrigin(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, want string }{
		{"", OriginRun},
		{OriginRun, OriginRun},
		{OriginTUI, OriginTUI},
		{OriginACP, OriginACP},
		{OriginPlan, OriginPlan},
	} {
		t.Run("origin="+c.in, func(t *testing.T) {
			t.Parallel()
			f := newFixture()
			f.approver = &fakeApprover{answers: []ApprovalResult{{Decision: DecisionAllowOnce}}}
			f.tools.add("write", &toolStub{prepare: func(_ context.Context, call ToolCall) (Prepared, error) {
				return Prepared{Call: call, Effect: EffectIdempotent, Approval: ApprovalAsk, Preview: "write a"}, nil
			}})
			f.provider.scripts = []streamFunc{
				callScript(FinishToolCalls, ToolCall{ID: "a", Name: "write", Arguments: `{"body":"x"}`}),
				textScript("done", event.Usage{}),
			}
			deps := f.deps()
			deps.Origin = c.in
			l, err := New(&f.cfg, &deps)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			out, err := l.Prompt(context.Background(), "S1", Input{Text: "go"})
			if err != nil {
				t.Fatalf("Prompt: %v", err)
			}
			for range out {
			}
			reqs := f.approver.reqs
			if len(reqs) != 1 || reqs[0].Origin != c.want {
				t.Fatalf("requests = %+v, want origin %q", reqs, c.want)
			}
		})
	}
}

// TestApprovalOriginRejected: an unknown origin fails New instead of being
// passed through to an approver.
func TestApprovalOriginRejected(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"headless", "web", "gateway", "mcp_serve", "TUI", " "} {
		if ValidOrigin(bad) {
			t.Errorf("ValidOrigin(%q) = true", bad)
		}
		f := newFixture()
		deps := f.deps()
		deps.Origin = bad
		if _, err := New(nil, &deps); !errors.Is(err, ErrOrigin) {
			t.Errorf("New(origin=%q) = %v, want ErrOrigin", bad, err)
		}
	}
}
