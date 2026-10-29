package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/loop/event"
)

type streamFlush struct {
	at   time.Time
	text string
}

// driveStream feeds n text deltas at step intervals of a fake clock, then tail,
// and records every flush together with the fake time it reached the adapter.
func driveStream(t *testing.T, n int, step time.Duration, tail ...event.Update) ([]streamFlush, replyOutcome) {
	t.Helper()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	clock := func() time.Time { now = now.Add(step); return now }
	wait := func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil }
	var calls []streamFlush
	flush := func(_ context.Context, text string) error {
		calls = append(calls, streamFlush{at: now, text: text})
		return nil
	}
	ch := make(chan event.Update, n+len(tail))
	for range n {
		ch <- event.Update{Kind: event.UpdateTextDelta, Text: "a"}
	}
	for i := range tail {
		ch <- tail[i]
	}
	close(ch)
	out := streamReplyUpdates(t.Context(), ch, clock, wait, flush)
	return calls, out
}

// checkGaps asserts that no two edits reached the adapter closer than the
// coalescer's pacing floor.
func checkGaps(t *testing.T, calls []streamFlush) {
	t.Helper()
	for i := 1; i < len(calls); i++ {
		if gap := calls[i].at.Sub(calls[i-1].at); gap < replyEditGap {
			t.Fatalf("edit %d arrived %s after edit %d (< %s)", i, gap, i-1, replyEditGap)
		}
	}
}

func TestStreamReplyBurstCoalesces(t *testing.T) {
	t.Parallel()
	calls, out := driveStream(t, 200, time.Millisecond)
	t.Logf("200 deltas/200ms: %d updates total", len(calls))
	if len(calls) > 3 {
		t.Fatalf("200 deltas in 200ms produced %d updates (%v), want <= 3", len(calls), calls)
	}
	if calls[0].text != "a" {
		t.Fatalf("first update = %q, want the first delta immediately", calls[0].text)
	}
	if out.Text != strings.Repeat("a", 200) {
		t.Fatalf("final text = %q, want all 200 deltas", out.Text)
	}
	if calls[len(calls)-1].text != out.Text {
		t.Fatalf("final update = %q, want %q", calls[len(calls)-1].text, out.Text)
	}
	checkGaps(t, calls)
}

// TestStreamReplyShortAnswerEdits: a ~300 character answer never needs more
// than four updates in total.
func TestStreamReplyShortAnswerEdits(t *testing.T) {
	t.Parallel()
	calls, out := driveStream(t, 300, time.Millisecond)
	t.Logf("300 character answer: %d updates total", len(calls))
	if len(calls) > 4 {
		t.Fatalf("300 character answer produced %d updates, want <= 4", len(calls))
	}
	if out.Text != strings.Repeat("a", 300) {
		t.Fatalf("final text has %d bytes, want 300", len(out.Text))
	}
	checkGaps(t, calls)
}

// TestStreamReplyCadenceOverLongStream: a 10 s stream edits roughly once per
// replyFlushEvery, never faster than replyEditGap.
func TestStreamReplyCadenceOverLongStream(t *testing.T) {
	t.Parallel()
	calls, out := driveStream(t, 100, 100*time.Millisecond)
	edits := len(calls) - 2 // first send and final flush are not cadence edits
	t.Logf("10s stream: %d updates, %d mid-stream edits", len(calls), edits)
	if want := 8; edits < want-1 || edits > want+1 {
		t.Fatalf("mid-stream edits = %d (%v), want %d ± 1", edits, calls, want)
	}
	if out.Text != strings.Repeat("a", 100) {
		t.Fatalf("final text = %q, want every delta", out.Text)
	}
	checkGaps(t, calls)
}

// TestStreamReplyProviderErrorIsSingleFinal: a failed turn ends with one error
// message, never a trailing partial text.
func TestStreamReplyProviderErrorIsSingleFinal(t *testing.T) {
	t.Parallel()
	stop := event.Update{Kind: event.UpdateStop, Stop: event.StopError, Detail: event.DetailProviderError}
	calls, out := driveStream(t, 5, time.Millisecond, stop)
	if !out.Failed || out.Detail != event.DetailProviderError {
		t.Fatalf("outcome = %+v, want a provider error", out)
	}
	if want := "damnbot: provider error (provider_error)"; out.Text != want {
		t.Fatalf("final text = %q, want %q", out.Text, want)
	}
	if last := calls[len(calls)-1].text; last != out.Text {
		t.Fatalf("last update = %q, want the error message %q", last, out.Text)
	}
	if len(calls) > 2 {
		t.Fatalf("error reply produced %d updates (%v), want the opening send plus one error", len(calls), calls)
	}
	checkGaps(t, calls)
}

// TestStreamReplyProviderErrorWithoutDeltas: nothing streamed, so the only
// update is the error message and no pacing wait is needed.
func TestStreamReplyProviderErrorWithoutDeltas(t *testing.T) {
	t.Parallel()
	stop := event.Update{Kind: event.UpdateStop, Stop: event.StopError, Detail: event.DetailProviderError}
	calls, out := driveStream(t, 0, time.Millisecond, stop)
	if len(calls) != 1 || calls[0].text != out.Text {
		t.Fatalf("updates = %v, want exactly the error message", calls)
	}
}
