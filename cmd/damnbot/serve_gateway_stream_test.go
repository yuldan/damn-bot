package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// streamStart is the fake epoch every cadence test measures from.
func streamStart() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

type streamFlush struct {
	at   time.Time
	text string
}

// driveStream feeds n text deltas at step intervals of a fake clock, then tail,
// and records every flush together with the fake time it reached the adapter.
// The pacer starts from a placeholder of len(gatewayAckText) at streamStart.
func driveStream(t *testing.T, n int, step time.Duration, tail ...event.Update) ([]streamFlush, replyOutcome) {
	t.Helper()
	now := streamStart()
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
	out := streamReplyUpdates(t.Context(), ch, clock, wait, flush, streamStart(), len(gatewayAckText))
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

// TestStreamReplyShortAnswerSingleFinalEdit: a 39-character answer streamed
// over ~6 s is edited exactly once, immediately when the channel closes.
func TestStreamReplyShortAnswerSingleFinalEdit(t *testing.T) {
	t.Parallel()
	const n, step = 39, 150 * time.Millisecond
	calls, out := driveStream(t, n, step)
	t.Logf("39 chars over %s: %d updates", time.Duration(n)*step, len(calls))
	if len(calls) != 1 {
		t.Fatalf("short answer produced %d updates (%v), want one final edit", len(calls), calls)
	}
	if out.Text != strings.Repeat("a", n) || calls[0].text != out.Text {
		t.Fatalf("final text = %q / %q, want all %d deltas", calls[0].text, out.Text, n)
	}
	if want := streamStart().Add(time.Duration(n) * step); !calls[0].at.Equal(want) {
		t.Fatalf("final edit at %s, want immediately at channel close %s", calls[0].at, want)
	}
}

// TestStreamReplyBurstCoalesces: 200 deltas inside 200 ms are one final edit.
func TestStreamReplyBurstCoalesces(t *testing.T) {
	t.Parallel()
	calls, out := driveStream(t, 200, time.Millisecond)
	t.Logf("200 deltas/200ms: %d updates", len(calls))
	if len(calls) != 1 || calls[0].text != strings.Repeat("a", 200) {
		t.Fatalf("updates = %v, want a single full final edit", calls)
	}
	if out.Text != strings.Repeat("a", 200) {
		t.Fatalf("final text = %q, want all 200 deltas", out.Text)
	}
}

// TestStreamReply300CharsAtMostFourUpdates: the measured target for a ~300
// character answer.
func TestStreamReply300CharsAtMostFourUpdates(t *testing.T) {
	t.Parallel()
	calls, out := driveStream(t, 300, time.Millisecond)
	t.Logf("300 character answer: %d updates", len(calls))
	if len(calls) > 4 {
		t.Fatalf("300 character answer produced %d updates, want <= 4", len(calls))
	}
	if out.Text != strings.Repeat("a", 300) {
		t.Fatalf("final text has %d bytes, want 300", len(out.Text))
	}
	checkGaps(t, calls)
}

// TestStreamReplyLongAnswerCadence: past replyShortChars the stream returns to
// the ≥1.2 s / ≥80-char cadence, never closer than replyEditGap.
func TestStreamReplyLongAnswerCadence(t *testing.T) {
	t.Parallel()
	calls, out := driveStream(t, 150, 100*time.Millisecond)
	t.Logf("150 chars over 15s: %d updates", len(calls))
	if len(calls) < 2 {
		t.Fatalf("long stream produced %d updates, want mid-stream edits plus final", len(calls))
	}
	if out.Text != strings.Repeat("a", 150) || calls[len(calls)-1].text != out.Text {
		t.Fatalf("final text = %q, want all 150 deltas", out.Text)
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
	if len(calls) != 1 || calls[0].text != out.Text {
		t.Fatalf("updates = %v, want exactly the error message", calls)
	}
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
