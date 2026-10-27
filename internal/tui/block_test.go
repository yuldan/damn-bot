package tui

import (
	"strings"
	"testing"
	"time"
)

// TestToolBlockAttributes pins the R1 block model (SPEC §13.1): every tool
// attribute survives folding and renders as plain text.
func TestToolBlockAttributes(t *testing.T) {
	t.Parallel()
	m, clk, _ := newTestApp(t)
	at := clk.Now()
	m.Update(StreamMsg{Update: Update{Kind: KindToolCallStart, ToolCallID: "c1", ToolName: "read_file", At: at}})
	m.Update(StreamMsg{Update: Update{Kind: KindToolCallArgs, ToolCallID: "c1", Text: `{"path":"internal/provider/http.go"}`, At: at.Add(time.Millisecond)}})
	m.Update(StreamMsg{Update: Update{Kind: KindToolCallEnd, ToolCallID: "c1", Text: `{"path":"internal/provider/http.go"}`, At: at.Add(2 * time.Millisecond)}})
	m.Update(StreamMsg{Update: Update{
		Kind: KindToolResult, ToolCallID: "c1", ActionID: 812, At: at.Add(120 * time.Millisecond),
		Text: "internal/provider/http.go #3f9a12c0 · L1-40/40 · utf-8 lf\n42 lines\n[spill: /home/u/.cache/damnbot/blobs/3f/3f9a (18200 chars)]",
	}})
	blocks := m.Blocks()
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	b := blocks[0]
	if b.Kind != BlockTool || b.ToolName != "read_file" || b.ToolCallID != "c1" {
		t.Fatalf("tool identity = %+v", b)
	}
	if b.Args != `{"path":"internal/provider/http.go"}` {
		t.Fatalf("args = %q", b.Args)
	}
	if b.Status != ToolOK || b.Tag != "3f9a12c0" || b.Size != 18200 || b.SpillSize != 18200 {
		t.Fatalf("status/tag/size = %q/%q/%d/%d", b.Status, b.Tag, b.Size, b.SpillSize)
	}
	if b.Spill != "/home/u/.cache/damnbot/blobs/3f/3f9a" || b.ActionID != 812 {
		t.Fatalf("spill/action = %q/%d", b.Spill, b.ActionID)
	}
	if b.Duration != 120*time.Millisecond {
		t.Fatalf("duration = %v, want 120ms", b.Duration)
	}
	view := m.View().Content
	for _, want := range []string{"tool read_file internal/provider/http.go", "#3f9a12c0", "✓", "120ms", "18.2k", "↶ 812", "spill: /home/u/.cache/damnbot/blobs/3f/3f9a"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

// TestToolBlockPending is the ⏸ state of an approval that has not answered.
func TestToolBlockPending(t *testing.T) {
	t.Parallel()
	m, clk, _ := newTestApp(t)
	at := clk.Now()
	m.Update(StreamMsg{Update: Update{Kind: KindToolCallStart, ToolCallID: "c2", ToolName: "shell", At: at}})
	m.Update(StreamMsg{Update: Update{Kind: KindToolCallEnd, ToolCallID: "c2", Status: "awaiting_approval", At: at.Add(time.Millisecond)}})
	if got := m.Blocks()[0].Status; got != ToolPending {
		t.Fatalf("status = %q, want pending", got)
	}
	if view := m.View().Content; !strings.Contains(view, "⏸") {
		t.Fatalf("view missing pending glyph:\n%s", view)
	}
}

// TestToolBlockFailureExitCode: ✗ carries the shell exit code and duration.
func TestToolBlockFailureExitCode(t *testing.T) {
	t.Parallel()
	m, clk, _ := newTestApp(t)
	at := clk.Now()
	m.Update(StreamMsg{Update: Update{Kind: KindToolCallStart, ToolCallID: "c3", ToolName: "shell", At: at}})
	m.Update(StreamMsg{Update: Update{
		Kind: KindToolResult, ToolCallID: "c3", At: at.Add(250 * time.Millisecond),
		Text: "[exit 1 · 250 ms · signal none · runner direct · status exited]\nboom",
	}})
	b := m.Blocks()[0]
	if b.Status != ToolFailed || b.ExitCode == nil || *b.ExitCode != 1 {
		t.Fatalf("status/exit = %q/%v", b.Status, b.ExitCode)
	}
	view := m.View().Content
	if !strings.Contains(view, "✗") || !strings.Contains(view, "exit 1") || !strings.Contains(view, "250ms") {
		t.Fatalf("view missing failure attributes:\n%s", view)
	}
}

// TestToolBlockErrorEnvelope marks a failed tool result when the loop drops
// ExecStatus but the content is an error envelope.
func TestToolBlockErrorEnvelope(t *testing.T) {
	t.Parallel()
	m, clk, _ := newTestApp(t)
	at := clk.Now()
	m.Update(StreamMsg{Update: Update{Kind: KindToolCallStart, ToolCallID: "c4", ToolName: "edit_file", At: at}})
	m.Update(StreamMsg{Update: Update{
		Kind: KindToolResult, ToolCallID: "c4", At: at.Add(time.Millisecond),
		Text: `{"ok":false,"error":{"code":"stale_read","message":"read the file first"}}`,
	}})
	b := m.Blocks()[0]
	if b.Status != ToolFailed {
		t.Fatalf("status = %q, want failed", b.Status)
	}
	if view := m.View().Content; !strings.Contains(view, "stale_read: read the file first") {
		t.Fatalf("view missing error preview:\n%s", view)
	}
}
