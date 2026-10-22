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
		if got := TurnInlineCap(32768, 1); got != 9830 {
			t.Fatalf("cap(32K, ratio 1) = %d, want 9830", got)
		}
		if got := TurnInlineCap(32768, 4); got != TurnInlineChars {
			t.Fatalf("cap(32K, ratio 4) = %d, want the 24.000 ceiling", got)
		}
		if got := TurnInlineCap(0, 4); got != TurnInlineChars {
			t.Fatalf("cap(unknown window) = %d, want 24.000", got)
		}

		// Three 4.000-char results fit the fixed cap but not a 32K window at
		// ratio 1: the third spills.
		tool := newContentTool("gen", repeated(InlineChars))
		scaled, err := NewDispatcher(&Config{
			Registry: mustReg(t, tool), Journal: &journalRec{}, Spill: newMemSpill(),
			Redactor: testRedactor(), Mode: ModeAuto, WindowTokens: 32768, CharsPerToken: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		for i := range 3 {
			out := scaled.Dispatch(context.Background(), Call{Name: "gen", Args: []byte(`{"body":"x"}`)})
			if out.Err != nil {
				t.Fatal(out.Err)
			}
			if wantSpill := i == 2; (out.Spilled != nil) != wantSpill {
				t.Fatalf("result %d: spilled=%t, want %t", i+1, out.Spilled != nil, wantSpill)
			}
		}

		fixed := func() *Dispatcher {
			d, err := NewDispatcher(&Config{
				Registry: mustReg(t, newContentTool("gen", repeated(InlineChars))),
				Journal:  &journalRec{}, Spill: newMemSpill(), Redactor: testRedactor(), Mode: ModeAuto,
			})
			if err != nil {
				t.Fatal(err)
			}
			return d
		}()
		for i := range 3 {
			if out := fixed.Dispatch(context.Background(), Call{Name: "gen", Args: []byte(`{"body":"x"}`)}); out.Spilled != nil {
				t.Fatalf("fixed cap spilled result %d", i+1)
			}
		}

		// Five 40.000-char results stay within 30% of the window in total.
		big, err := NewDispatcher(&Config{
			Registry: mustReg(t, newContentTool("big", repeated(40000))),
			Journal:  &journalRec{}, Spill: newMemSpill(), Redactor: testRedactor(), Mode: ModeAuto,
			WindowTokens: 32768, CharsPerToken: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		total := 0
		for range 5 {
			out := big.Dispatch(context.Background(), Call{Name: "big", Args: []byte(`{"body":"x"}`)})
			if out.Err != nil || out.Spilled == nil {
				t.Fatalf("40K result not spilled: %+v", out)
			}
			total += utf8.RuneCountInString(out.Content)
		}
		if total > TurnInlineCap(32768, 1) {
			t.Fatalf("inline total = %d, want ≤ 30%% of a 32K window", total)
		}
	})
}
