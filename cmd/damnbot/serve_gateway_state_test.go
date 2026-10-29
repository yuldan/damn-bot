package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// TestECSTATE012FlushCursorResetsPerTurn pins "EC-STATE-012"
// (EDGE-CASES-hermes.md): the reply flush cursor is per turn. Turn two starts
// at the same byte count as turn one ended with; a cursor carried over would
// consider it already flushed and pace its final edit behind turn one. With a
// fresh cursor the new turn flushes immediately and no wait is requested.
func TestECSTATE012FlushCursorResetsPerTurn(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	var waited []time.Duration
	wait := func(ctx context.Context, d time.Duration) error {
		waited = append(waited, d)
		return ctx.Err()
	}
	run := func(start time.Time, at []time.Time, parts ...string) ([]string, int) {
		t.Helper()
		next := 0
		now := func() time.Time {
			if next >= len(at) {
				return at[len(at)-1]
			}
			at := at[next]
			next++
			return at
		}
		ch := make(chan event.Update, len(parts))
		for _, p := range parts {
			ch <- event.Update{Kind: event.UpdateTextDelta, Text: p}
		}
		close(ch)
		var flushed []string
		flush := func(_ context.Context, text string) error {
			flushed = append(flushed, text)
			return nil
		}
		before := len(waited)
		streamReplyUpdates(t.Context(), ch, now, wait, flush, start, 0)
		return flushed, len(waited) - before
	}

	long := strings.Repeat("A", 200)
	// Turn one reaches 400 bytes and ends with one paced mid-stream edit.
	if flushed, _ := run(base, []time.Time{base, base.Add(2 * time.Second)}, long, long); len(flushed) == 0 {
		t.Fatal("turn one flushed nothing")
	}
	// Turn two has the same final size as turn one: a stale cursor would skip
	// its first observation and make the final edit wait a full edit gap.
	second := strings.Repeat("B", 400)
	flushed, waits := run(base.Add(2*time.Second), []time.Time{base.Add(2 * time.Second)}, second)
	if waits != 0 {
		t.Fatalf("turn two waited %v before its final flush; cursor leaked from turn one", waits)
	}
	if len(flushed) != 1 || flushed[0] != second {
		t.Fatalf("turn two flushed %d update(s) %q, want its own text", len(flushed), flushed)
	}
}
