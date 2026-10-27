package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestStatusBarContents pins the minimal SPEC §13.1 status line: model,
// iteration, turn/session cost, and the FALLBACK badge only while the visible
// provider fallback (P7) is active.
func TestStatusBarContents(t *testing.T) {
	t.Parallel()
	m, clk, _ := newTestApp(t)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	cost := 0.04
	m.Update(StreamMsg{Update: Update{
		Kind: KindUsage, Usage: &Usage{Iterations: 7, Input: 10, Output: 5, CostUSD: &cost}, At: clk.Now(),
	}})
	status := m.statusBar()
	for _, want := range []string{"fake/model", "iter 7", "turn $0.04 / session $0.04"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status bar missing %q: %q", want, status)
		}
	}
	if strings.Contains(status, "FALLBACK") {
		t.Fatalf("status bar shows FALLBACK without a fallback: %q", status)
	}

	m.Update(StreamMsg{Update: Update{
		Kind: KindNotice, Text: "provider fallback: a/x -> b/y", ToolName: "fallback", At: clk.Now(),
	}})
	if got := m.statusBar(); !strings.Contains(got, "FALLBACK") {
		t.Fatalf("status bar lost the FALLBACK badge: %q", got)
	}

	m.Update(StreamMsg{Update: Update{Kind: KindStop, Stop: "end_turn", Detail: "completed", At: clk.Now()}})
	next := 0.06
	m.Update(StreamMsg{Update: Update{
		Kind: KindUsage, Usage: &Usage{Iterations: 2, CostUSD: &next}, At: clk.Now(),
	}})
	status = m.statusBar()
	if !strings.Contains(status, "turn $0.06 / session $0.10") {
		t.Fatalf("session cost did not accumulate across turns: %q", status)
	}
}

// TestStatusBarUnknownCost: a provider that reports no cost stays an explicit
// dash, never a fabricated $0.00.
func TestStatusBarUnknownCost(t *testing.T) {
	t.Parallel()
	m, clk, _ := newTestApp(t)
	m.Update(StreamMsg{Update: Update{Kind: KindUsage, Usage: &Usage{Iterations: 1}, At: clk.Now()}})
	status := m.statusBar()
	if !strings.Contains(status, "iter 1") || !strings.Contains(status, "turn — / session —") {
		t.Fatalf("status bar = %q, want unknown costs as dashes", status)
	}
	if strings.Contains(status, "$0.00") {
		t.Fatalf("status bar invented a zero cost: %q", status)
	}
}
