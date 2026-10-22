package tools

import (
	"context"
	"testing"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestTurnAggregateScaled is the SPEC P5 amendment / GAP-COST-08 gate: the
// turn aggregate is min(24.000, 0,30 × context_window_tok × char/token), so a
// 32K model can never receive five 40.000-char results inline.
func TestTurnAggregateScaled(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-FIO-006", "F1", "covered") {
		return
	}
	t.Run("EC-FIO-006", func(t *testing.T) {
		t.Parallel()
		t.Run("cap formula", testInlineCapFormula)
		t.Run("window forces a spill", testInlineSpillUnderWindow)
		t.Run("fixed cap keeps three results", testInlineFixedCapKeepsThree)
		t.Run("aggregate stays in the window", testInlineAggregateWithinWindow)
	})
}

// testInlineCapFormula: an unknown window and a wide ratio keep the fixed
// ceiling.
func testInlineCapFormula(t *testing.T) {
	t.Parallel()
	if got := TurnInlineCap(32768, 1); got != 9830 {
		t.Fatalf("cap(32K, ratio 1) = %d, want 9830", got)
	}
	if got := TurnInlineCap(32768, 4); got != TurnInlineChars {
		t.Fatalf("cap(32K, ratio 4) = %d, want the 24.000 ceiling", got)
	}
	if got := TurnInlineCap(0, 4); got != TurnInlineChars {
		t.Fatalf("cap(unknown window) = %d, want 24.000", got)
	}
}

// testInlineSpillUnderWindow: three 4.000-char results fit the fixed cap but
// not a 32K window at ratio 1, so the third spills.
func testInlineSpillUnderWindow(t *testing.T) {
	t.Parallel()
	d := inlineDispatcher(t, "gen", repeated(InlineChars), 32768, 1)
	for i := range 3 {
		out := d.Dispatch(context.Background(), Call{Name: "gen", Args: []byte(`{"body":"x"}`)})
		if out.Err != nil {
			t.Fatal(out.Err)
		}
		if wantSpill := i == 2; (out.Spilled != nil) != wantSpill {
			t.Fatalf("result %d: spilled=%t, want %t", i+1, out.Spilled != nil, wantSpill)
		}
	}
}

// testInlineFixedCapKeepsThree: with no window at all the fixed cap still holds
// the same three results inline.
func testInlineFixedCapKeepsThree(t *testing.T) {
	t.Parallel()
	d := inlineDispatcher(t, "gen", repeated(InlineChars), 0, 0)
	for i := range 3 {
		if out := d.Dispatch(context.Background(), Call{Name: "gen", Args: []byte(`{"body":"x"}`)}); out.Spilled != nil {
			t.Fatalf("fixed cap spilled result %d", i+1)
		}
	}
}

// testInlineAggregateWithinWindow: five 40.000-char results all spill and the
// inline total stays within 30 % of a 32K window.
func testInlineAggregateWithinWindow(t *testing.T) {
	t.Parallel()
	d := inlineDispatcher(t, "big", repeated(40000), 32768, 1)
	total := 0
	for range 5 {
		out := d.Dispatch(context.Background(), Call{Name: "big", Args: []byte(`{"body":"x"}`)})
		if out.Err != nil || out.Spilled == nil {
			t.Fatalf("40K result not spilled: %+v", out)
		}
		total += utf8.RuneCountInString(out.Content)
	}
	if total > TurnInlineCap(32768, 1) {
		t.Fatalf("inline total = %d, want ≤ 30%% of a 32K window", total)
	}
}

// inlineDispatcher builds a dispatcher whose single content tool emits
// content; window is 0 when the caller wants the fixed cap.
func inlineDispatcher(t *testing.T, name, content string, window int, ratio float64) *Dispatcher {
	t.Helper()
	d, err := NewDispatcher(&Config{
		Registry: mustReg(t, newContentTool(name, content)),
		Journal:  &journalRec{}, Spill: newMemSpill(), Redactor: testRedactor(),
		Mode: ModeAuto, WindowTokens: window, CharsPerToken: ratio,
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	return d
}
