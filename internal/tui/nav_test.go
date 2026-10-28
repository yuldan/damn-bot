package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// navApp builds a model with three blocks: user, multi-line assistant, and an
// undoable tool block with a spill path.
func navApp(t *testing.T) (*Model, *fakeSessions, *fakeClipboard) {
	t.Helper()
	clk, runner := newFakeClock(), &fakeRunner{}
	host := testHost(clk, runner, &fakeConfig{cfg: Config{Model: "fake/model"}})
	m, err := NewApp("sess-nav", host)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	at := clk.Now()
	m.appendBlock(&Block{Kind: BlockUser, Text: "hello", At: at})
	m.appendBlock(&Block{Kind: BlockAssistant, Text: "line one\nline two", At: at})
	m.appendBlock(&Block{
		Kind: BlockTool, ToolName: "read_file", ToolCallID: "c1", Status: ToolOK,
		Text: "result", Spill: "/data/blobs/3f/aa", SpillSize: 18200, ActionID: 12, At: at,
	})
	sessions, ok := host.Sessions.(*fakeSessions)
	if !ok {
		t.Fatalf("host sessions = %T", host.Sessions)
	}
	clip, ok := host.Clipboard.(*fakeClipboard)
	if !ok {
		t.Fatalf("host clipboard = %T", host.Clipboard)
	}
	return m, sessions, clip
}

func navKey(code rune, mod tea.KeyMod) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code, Mod: mod} }

// TestNavMovesAndFolds: ctrl+k selects the newest block, k moves up, enter
// folds and unfolds the selected block.
func TestNavMovesAndFolds(t *testing.T) {
	t.Parallel()
	m, _, _ := navApp(t)
	m.Update(navKey('k', tea.ModCtrl))
	if !m.navMode || m.navIndex != 2 {
		t.Fatalf("ctrl+k = navMode %v index %d, want newest block", m.navMode, m.navIndex)
	}
	if !strings.Contains(m.View().Content, "▸") {
		t.Fatalf("view has no selection marker")
	}
	m.Update(navKey('k', 0))
	if m.navIndex != 1 {
		t.Fatalf("k selected %d, want 1", m.navIndex)
	}
	if !strings.Contains(m.View().Content, "line one") || !strings.Contains(m.View().Content, "line two") {
		t.Fatalf("expanded assistant block lost a line")
	}
	m.Update(navKey(tea.KeyEnter, 0))
	view := m.View().Content
	if strings.Contains(view, "line two") {
		t.Fatalf("enter did not fold the selected block:\n%s", view)
	}
	if !strings.Contains(view, "line one …") {
		t.Fatalf("folded block has no ellipsis marker:\n%s", view)
	}
	m.Update(navKey(tea.KeyEnter, 0))
	if !strings.Contains(m.View().Content, "line two") {
		t.Fatalf("second enter did not unfold the block")
	}
	m.Update(navKey('k', tea.ModCtrl))
	if m.navMode {
		t.Fatalf("ctrl+k did not close navigation")
	}
}

// TestNavCopyBlock: y copies the plain tool block including the spill path.
func TestNavCopyBlock(t *testing.T) {
	t.Parallel()
	m, _, clip := navApp(t)
	m.Update(navKey('k', tea.ModCtrl)) // tool block
	cmd := m.handleKey(navKey('y', 0))
	if cmd == nil {
		t.Fatalf("y returned no command")
	}
	m.Update(cmd())
	got := clip.copied()
	for _, want := range []string{"tool read_file", "spill: /data/blobs/3f/aa"} {
		if !strings.Contains(got, want) {
			t.Fatalf("clipboard %q missing %q", got, want)
		}
	}
	if !strings.Contains(m.View().Content, "copied") {
		t.Fatalf("no copy notice")
	}
}

// TestNavCopyUnavailable: a missing platform utility is a visible reason, not
// a success claim.
func TestNavCopyUnavailable(t *testing.T) {
	t.Parallel()
	m, _, clip := navApp(t)
	clip.err = errors.New("xclip: not found")
	m.Update(navKey('k', tea.ModCtrl))
	m.Update(m.handleKey(navKey('y', 0))())
	if !strings.Contains(m.View().Content, "clipboard unavailable: xclip: not found") {
		t.Fatalf("clipboard error was hidden:\n%s", m.View().Content)
	}
}

// TestNavBlobAndUndo: o prints the spill path, u calls the real action id, and
// a block without an action is refused with a reason.
func TestNavBlobAndUndo(t *testing.T) {
	t.Parallel()
	m, sessions, _ := navApp(t)
	m.Update(navKey('k', tea.ModCtrl))
	m.Update(navKey('o', 0))
	if !strings.Contains(m.View().Content, "blob /data/blobs/3f/aa") {
		t.Fatalf("o did not print the spill path:\n%s", m.View().Content)
	}
	cmd := m.handleKey(navKey('u', 0))
	if cmd == nil {
		t.Fatalf("u returned no command")
	}
	m.Update(cmd())
	if got := sessions.lastCall(); got != "undo:sess-nav:a12" {
		t.Fatalf("undo call = %q, want the block action id", got)
	}
	if !strings.Contains(m.View().Content, "a1 undone") {
		t.Fatalf("undo notice missing")
	}

	// Assistant block has no action.
	m.Update(navKey('k', 0))
	m.Update(navKey('u', 0))
	last := m.blocks[len(m.blocks)-1]
	if !strings.Contains(last.Text, "no undoable action") {
		t.Fatalf("undoable-less block was not refused: %+v", last)
	}
}

// TestNavForkGap: f is disabled with the real reason and never fakes a fork.
func TestNavForkGap(t *testing.T) {
	t.Parallel()
	m, sessions, _ := navApp(t)
	m.Update(navKey('k', tea.ModCtrl))
	m.Update(navKey('f', 0))
	if !strings.Contains(m.View().Content, "fork per block is unavailable") ||
		!strings.Contains(m.View().Content, "no turn/seq") {
		t.Fatalf("fork gap not explained:\n%s", m.View().Content)
	}
	if sessions.lastCall() != "" {
		t.Fatalf("f called the fork host anyway: %q", sessions.lastCall())
	}
}

// TestNavIgnoresTyping: while navigating, letters do not leak into the prompt.
func TestNavIgnoresTyping(t *testing.T) {
	t.Parallel()
	m, _, _ := navApp(t)
	m.Update(navKey('k', tea.ModCtrl))
	m.Update(navKey('x', 0))
	if got := m.input.Value(); got != "" {
		t.Fatalf("navigation inserted %q into the prompt", got)
	}
	m.Update(navKey(tea.KeyEsc, 0))
	if m.navMode {
		t.Fatalf("esc did not close navigation")
	}
}
