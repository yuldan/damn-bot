package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// applyBatch feeds one batch message and returns the next command.
func applyBatch(t *testing.T, m *Model, msg tea.Msg) tea.Cmd {
	t.Helper()
	_, cmd := m.Update(msg)
	return cmd
}

// TestStreamBatchWindow: the first update is delivered immediately (P17) and
// the remaining deltas are folded into one 33 ms window (SPEC §13.1).
func TestStreamBatchWindow(t *testing.T) {
	t.Parallel()
	m, clk, _ := newTestApp(t)
	ch := make(chan Update, 16)
	for range 5 {
		ch <- Update{Kind: KindTextDelta, Text: "x", At: clk.Now()}
	}

	first := applyBatch(t, m, streamReadyMsg{ch})()
	batch, ok := first.(streamBatchMsg)
	if !ok || batch.done || len(batch.updates) != 1 {
		t.Fatalf("first message = %#v, want one immediate update", first)
	}

	start := time.Now()
	second := applyBatch(t, m, batch)()
	elapsed := time.Since(start)
	next, ok := second.(streamBatchMsg)
	if !ok || next.done || len(next.updates) != 4 {
		t.Fatalf("second message = %#v, want the remaining 4-update window", second)
	}
	if elapsed < streamWindow-8*time.Millisecond {
		t.Fatalf("window returned after %v, want >= %v", elapsed, streamWindow)
	}
	close(ch)
	done := applyBatch(t, m, next)()
	if b, ok := done.(streamBatchMsg); !ok || !b.done {
		t.Fatalf("closed stream = %#v, want done", done)
	}
}

// TestStreamBatchFrames counts model folds (one per View-worthy window) for a
// burst of 100 deltas: the transcript must be assembled in ≤3 batches, never
// once per token.
func TestStreamBatchFrames(t *testing.T) {
	t.Parallel()
	m, clk, _ := newTestApp(t)
	const deltas = 100
	ch := make(chan Update, deltas)
	for range deltas {
		ch <- Update{Kind: KindTextDelta, Text: "x", At: clk.Now()}
	}
	close(ch)

	frames := 0
	msg := applyBatch(t, m, streamReadyMsg{ch})()
	for {
		batch, ok := msg.(streamBatchMsg)
		if !ok {
			t.Fatalf("message = %#v, want streamBatchMsg", msg)
		}
		next := applyBatch(t, m, batch)
		frames++
		_ = m.View()
		if batch.done {
			break
		}
		msg = next()
	}
	if frames > 3 {
		t.Fatalf("%d deltas produced %d frames, want ≤3 batches", deltas, frames)
	}
	blocks := m.Blocks()
	if len(blocks) != 1 || blocks[0].Text != strings.Repeat("x", deltas) {
		t.Fatalf("transcript = %+v, want one %d-char assistant block", blocks, deltas)
	}
}
