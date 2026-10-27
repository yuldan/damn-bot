package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// TestPlanModeDeferredNoActivation (F-1): a plan-mode denial must happen
// before the module runtime is asked to activate the owning module. Both
// entry points are covered: the tool name directly and tool_invoke.
//
//nolint:gocognit,gocyclo // one assertion per entry point and control
func TestPlanModeDeferredNoActivation(t *testing.T) {
	t.Parallel()
	defs := []DeferredTool{
		deferredDef("browser_write", "browser", "non_idempotent"),
		deferredDef("browser_read", "browser", "read"),
	}
	t.Run("direct", func(t *testing.T) {
		t.Parallel()
		f := newDeferredFixture(t, defs, ModeAuto, nil)
		out := f.d.Dispatch(context.Background(), Call{
			TurnID: "t1", CallID: "w1", Name: "browser_write",
			Args: json.RawMessage(`{"q":"x"}`), PlanMode: true,
		})
		if out.Err == nil || !out.Denied {
			t.Fatalf("write = %+v, want plan-mode denial", out)
		}
		if got := f.rt.ensureCount(); got != 0 {
			t.Fatalf("plan denial activated the module %d times", got)
		}
		if got := f.mod.calls(); got != 0 {
			t.Fatalf("module ran %d times", got)
		}
		if dc, rc := f.j.counts(); dc != 0 || rc != 0 {
			t.Fatalf("journal = %d/%d", dc, rc)
		}
	})
	t.Run("tool_invoke", func(t *testing.T) {
		t.Parallel()
		f := newDeferredFixture(t, defs, ModeAuto, nil)
		out := f.d.Dispatch(context.Background(), Call{
			TurnID: "t1", CallID: "w2", Name: ToolInvokeName,
			Args:     json.RawMessage(`{"name":"browser_write","arguments":{"q":"x"}}`),
			PlanMode: true,
		})
		if out.Err == nil || !out.Denied {
			t.Fatalf("invoke = %+v, want plan-mode denial", out)
		}
		if got := f.rt.ensureCount(); got != 0 {
			t.Fatalf("plan denial activated the module %d times", got)
		}
		if dc, rc := f.j.counts(); dc != 0 || rc != 0 {
			t.Fatalf("journal = %d/%d", dc, rc)
		}
	})
	t.Run("read control", func(t *testing.T) {
		t.Parallel()
		f := newDeferredFixture(t, defs, ModeAuto, nil)
		out := f.d.Dispatch(context.Background(), Call{
			TurnID: "t1", CallID: "r1", Name: "browser_read",
			Args: json.RawMessage(`{"q":"x"}`), PlanMode: true,
		})
		if out.Err != nil {
			t.Fatalf("read: %v", out.Err)
		}
		if got := f.rt.ensureCount(); got != 1 {
			t.Fatalf("read activated %d times, want 1", got)
		}
		if got := f.mod.calls(); got != 1 {
			t.Fatalf("read ran %d times, want 1", got)
		}
		if dc, rc := f.j.counts(); dc != 1 || rc != 1 {
			t.Fatalf("journal = %d/%d, want 1/1", dc, rc)
		}
	})
}
