package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// sizedApp assembles a model with a known terminal width and the default test
// height of 12 rows, so bodyHeight() is 8 transcript lines (header, status
// bar, input, footer = 4).
func sizedApp(t *testing.T, width int) (*Model, *fakeClock) {
	t.Helper()
	m, clk, _ := newTestApp(t)
	m.Update(tea.WindowSizeMsg{Width: width, Height: 12})
	return m, clk
}

// TestTranscriptRendersOnlyViewport: 40 one-line blocks at a 12-row terminal
// render exactly the 8 transcript lines, newest first; older blocks are never
// formatted.
func TestTranscriptRendersOnlyViewport(t *testing.T) {
	t.Parallel()
	m, clk := sizedApp(t, 80)
	at := clk.Now()
	for i := range 40 {
		m.appendBlock(&Block{Kind: BlockAssistant, Text: fmt.Sprintf("line-%02d", i), At: at})
	}
	view := m.View().Content
	if got, want := strings.Count(view, "line-"), m.bodyHeight(); got != want {
		t.Fatalf("rendered %d transcript lines, want the %d-line viewport:\n%s", got, want, view)
	}
	if strings.Contains(view, "line-00") || strings.Contains(view, "line-31") {
		t.Fatalf("view rendered blocks below the window:\n%s", view)
	}
	if !strings.Contains(view, "line-39") {
		t.Fatalf("view lost the newest block:\n%s", view)
	}
}

// TestScrollPreservesContent: paging up reaches the first block, paging down
// returns to the byte-identical tail frame, and the offset never goes negative.
func TestScrollPreservesContent(t *testing.T) {
	t.Parallel()
	m, clk := sizedApp(t, 80)
	at := clk.Now()
	for i := range 40 {
		m.appendBlock(&Block{Kind: BlockAssistant, Text: fmt.Sprintf("line-%02d", i), At: at})
	}
	bottom := m.View().Content
	for range 10 {
		m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	}
	top := m.View().Content
	if top == bottom {
		t.Fatalf("pgup did not move the viewport")
	}
	if !strings.Contains(top, "line-00") {
		t.Fatalf("scrolling up did not reach the first block:\n%s", top)
	}
	if strings.Contains(top, "line-39") {
		t.Fatalf("the tail is still visible at the top:\n%s", top)
	}
	for range 10 {
		m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	if m.scroll != 0 {
		t.Fatalf("scroll = %d after paging past the tail, want 0", m.scroll)
	}
	if got := m.View().Content; got != bottom {
		t.Fatalf("scroll round trip changed the tail frame:\nbefore:\n%s\nafter:\n%s", bottom, got)
	}
}

// TestSpillOutsideViewportNotRendered: a huge block above the window costs no
// rendering; scrolling it into view renders only the window-sized tail.
func TestSpillOutsideViewportNotRendered(t *testing.T) {
	t.Parallel()
	m, clk := sizedApp(t, 100)
	at := clk.Now()
	m.appendBlock(&Block{Kind: BlockAssistant, Text: strings.TrimSuffix(strings.Repeat("spill-secret line\n", 5000), "\n"), At: at})
	for i := range 40 {
		m.appendBlock(&Block{Kind: BlockAssistant, Text: fmt.Sprintf("line-%02d", i), At: at})
	}
	if view := m.View().Content; strings.Contains(view, "spill-secret") {
		t.Fatalf("off-screen spill block was rendered:\n%s", view)
	}
	for range 50 {
		m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	}
	top := m.View().Content
	if got, want := strings.Count(top, "spill-secret"), m.bodyHeight(); got != want {
		t.Fatalf("spill block in view rendered %d lines, want the %d-line window:\n%s", got, want, top)
	}
	if strings.Contains(top, "line-39") {
		t.Fatalf("scrolling to the first block kept the tail:\n%s", top)
	}
}

// TestAppendWhileScrolledKeepsContent: a reader scrolled up is not dragged
// forward by new stream blocks.
func TestAppendWhileScrolledKeepsContent(t *testing.T) {
	t.Parallel()
	m, clk := sizedApp(t, 80)
	at := clk.Now()
	for i := range 40 {
		m.appendBlock(&Block{Kind: BlockAssistant, Text: fmt.Sprintf("line-%02d", i), At: at})
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	before := m.View().Content
	m.appendBlock(&Block{Kind: BlockAssistant, Text: "line-40", At: at})
	after := m.View().Content
	if before != after {
		t.Fatalf("a new block moved the scrolled view:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestTranscriptWithoutSizeRendersAll keeps the first frame and pure-model
// tests working before WindowSizeMsg arrives.
func TestTranscriptWithoutSizeRendersAll(t *testing.T) {
	t.Parallel()
	m, clk, _ := newTestApp(t)
	at := clk.Now()
	for i := range 40 {
		m.appendBlock(&Block{Kind: BlockAssistant, Text: fmt.Sprintf("line-%02d", i), At: at})
	}
	if got := strings.Count(m.View().Content, "line-"); got != 40 {
		t.Fatalf("rendered %d of 40 blocks with an unknown terminal size", got)
	}
}
