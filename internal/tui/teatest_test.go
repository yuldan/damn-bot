package tui

import (
	"bytes"
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/teatest/v2"
)

// TestTeatestStreamRendersDelta is the program-level harness of the package:
// teatest drives a real bubbletea program over an in-memory pipe (no PTY), the
// test injects one translated loop update, and the frame that carries it must
// contain the text in stream order.
func TestTeatestStreamRendersDelta(t *testing.T) {
	m, clk, _ := newTestApp(t)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	at := clk.Now()
	tm.Send(StreamMsg{Update: Update{Kind: KindTextDelta, Text: "teatest-delta", At: at}})
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("teatest-delta"))
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))
	if err := tm.Quit(); err != nil {
		t.Fatalf("quit: %v", err)
	}
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
	final, ok := tm.FinalModel(t).(*Model)
	if !ok {
		t.Fatalf("final model is not *Model")
	}
	if got := final.RenderedAt(); got.Before(at) {
		t.Fatalf("renderedAt %v before Update.At %v", got, at)
	}
}
