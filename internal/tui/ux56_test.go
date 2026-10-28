package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestNoColorZeroEscape: with the plain render mode resolved at assembly, every
// screen state is free of ESC bytes even with a colored theme selected.
func TestNoColorZeroEscape(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestApp(t)
	m.host.Config = &fakeConfig{cfg: Config{Model: "fake/model", Theme: "high-contrast", NoColor: true}}
	m.noColor = true
	m.theme, _ = ThemeByName("high-contrast")
	m.styles = newStyles(&m.theme, true)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.appendBlock(&Block{Kind: BlockUser, Text: "halo"})
	m.appendBlock(&Block{Kind: BlockTool, ToolName: "read_file", Status: ToolOK, Spill: "/data/blobs/x", SpillSize: 200})
	m.appendBlock(&Block{Kind: BlockNotice, Text: "notice"})
	states := []string{m.View().Content}
	m.palette = true
	states = append(states, m.View().Content)
	m.palette = false
	m.confirm = &pendingWrite{kind: "export", path: "out.txt", body: []byte("x")}
	states = append(states, m.View().Content)
	m.confirm = nil
	m.Update(ApprovalMsg{Request: ApprovalRequest{Tool: "shell", Preview: "+ x", Effect: "non_idempotent"}, Reply: make(chan ApprovalDecision, 1)})
	states = append(states, m.View().Content)
	for i, view := range states {
		if strings.ContainsRune(view, 0x1b) {
			t.Fatalf("state %d contains ESC:\n%q", i, view)
		}
	}
	if strings.ContainsRune(m.styles.header.Render("x"), 0x1b) {
		t.Fatalf("no-color mode rendered a styled header")
	}
}

func TestNoColorWinsOverTheme(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestApp(t)
	if !strings.ContainsRune(m.styles.header.Render("x"), 0x1b) {
		t.Fatalf("default theme rendered no ANSI token")
	}
	m.noColor = true
	m.styles = newStyles(&m.theme, true)
	m.input.SetValue("/theme high-contrast")
	m.submit()
	if m.theme.Name != "high-contrast" {
		t.Fatalf("theme = %q", m.theme.Name)
	}
	if strings.ContainsRune(m.styles.header.Render("x"), 0x1b) || strings.ContainsRune(m.View().Content, 0x1b) {
		t.Fatalf("NO_COLOR lost to the colored theme")
	}
}

// TestResizeCoalesces: 50 resizes in one burst produce zero changed frames and
// exactly one committed layout after the 16 ms clock debounce.
func TestResizeCoalesces(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestApp(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	baseApplied := m.resizeApplied
	baseView := m.View().Content
	var last tea.Cmd
	changed := 0
	for i := range 50 {
		_, cmd := m.Update(tea.WindowSizeMsg{Width: 80 - i, Height: 24})
		last = cmd
		if m.View().Content != baseView {
			changed++
		}
	}
	if changed != 0 {
		t.Fatalf("intermediate resize rendered %d changed frames", changed)
	}
	if m.resizeApplied != baseApplied {
		t.Fatalf("intermediate resize committed layout %d times", m.resizeApplied-baseApplied)
	}
	if last == nil {
		t.Fatalf("resize returned no debounce command")
	}
	m.Update(last())
	if got := m.resizeApplied - baseApplied; got != 1 {
		t.Fatalf("settled resize committed %d layouts, want 1", got)
	}
	if m.width != 31 || m.height != 24 {
		t.Fatalf("settled size = %dx%d", m.width, m.height)
	}
	if m.View().Content == baseView {
		t.Fatalf("settled layout did not change the frame")
	}
}

// TestResizeNegativeControl: without the debounce the same storm commits every
// resize, proving the coalescer is load-bearing.
func TestResizeNegativeControl(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestApp(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.resizeDebounce = 0
	before := m.resizeApplied
	for i := range 50 {
		m.Update(tea.WindowSizeMsg{Width: 60 + i, Height: 24})
	}
	if got := m.resizeApplied - before; got != 50 {
		t.Fatalf("uncoalesced storm committed %d layouts, want 50", got)
	}
}

func TestNewAppHonorsNoColor(t *testing.T) {
	t.Parallel()
	clk, runner := newFakeClock(), &fakeRunner{}
	m, err := NewApp("sess-nc", testHost(clk, runner, &fakeConfig{cfg: Config{
		Model: "fake/model", Theme: "high-contrast", NoColor: true,
	}}))
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	if !m.noColor {
		t.Fatalf("NewApp dropped NoColor")
	}
	if view := m.View().Content; strings.ContainsRune(view, 0x1b) {
		t.Fatalf("first frame contains ESC")
	}
}
