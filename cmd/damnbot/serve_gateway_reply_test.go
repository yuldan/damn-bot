package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway/telegram"
	"github.com/damnbot/damnbot/internal/loop/event"
)

// TestTimestampPrefixNeverDoubles pins GW-020.
func TestTimestampPrefixNeverDoubles(t *testing.T) {
	prefix := timestampPrefix(time.Date(2026, 10, 30, 12, 34, 0, 0, time.UTC))
	if prefix != "[12:34] " {
		t.Fatalf("prefix = %q", prefix)
	}
	once := withTimestamp("hello", prefix)
	if once != "[12:34] hello" {
		t.Fatalf("once = %q", once)
	}
	if twice := withTimestamp(once, prefix); twice != once {
		t.Fatalf("double prefix = %q", twice)
	}
	if got := withTimestamp("hello", ""); got != "hello" {
		t.Fatalf("empty prefix changed text: %q", got)
	}
}

// TestHumanDelayWindow pins GW-007's 0.3–1.5 s bound and the disabled/approval
// zero path.
func TestHumanDelayWindow(t *testing.T) {
	for _, tc := range []struct {
		draw time.Duration
		want time.Duration
	}{
		{0, humanDelayMin},
		{humanDelayMax, humanDelayMax},
		{9999 * time.Hour, humanDelayMax},
	} {
		if got := humanDelayAmount(func() time.Duration { return tc.draw }); got != tc.want {
			t.Errorf("humanDelayAmount(%v) = %v, want %v", tc.draw, got, tc.want)
		}
	}
	sends := newCommandSends(t)
	gw := newTestGateway(t, sends.url)
	waited := 0
	gw.delayWait = func(context.Context, time.Duration) error { waited++; return nil }
	if err := gw.humanPace(t.Context(), false); err != nil || waited != 0 {
		t.Fatalf("disabled delay waited %d, err=%v", waited, err)
	}
	if err := gw.humanPace(t.Context(), true); err != nil || waited != 1 {
		t.Fatalf("enabled delay waited %d, err=%v", waited, err)
	}
	waited = 0
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _ = gw.feedback(ctx, "1", "", "shell", "rm")
	if waited != 0 {
		t.Fatalf("approval path waited %d human delays, want 0", waited)
	}
}

// TestFooterToggleChangesRenderedReply: the final reply gains the runtime
// footer only when /footer is on.
func TestFooterToggleChangesRenderedReply(t *testing.T) {
	sends := newCommandSends(t)
	gw := newTestGateway(t, sends.url)
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	gw.now = func() time.Time { return at }
	gw.modelRef = "openai/gpt-fake"
	cost := 0.001
	stream := func(update string) {
		ch := make(chan event.Update, 2)
		ch <- event.Update{Kind: event.UpdateTextDelta, Text: "hello"}
		ch <- event.Update{Kind: event.UpdateStop, Stop: event.StopEndTurn, Detail: event.DetailCompleted,
			Usage: &event.Usage{Iterations: 2, CostUSD: &cost}}
		close(ch)
		in := &telegram.Inbound{Platform: "telegram", ChatID: "600", SenderID: "7788", UpdateID: update, Text: "hi"}
		if err := gw.streamReply(t.Context(), in, ch, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	stream("r1")
	plain := sends.wait(t, "hello")
	if strings.Contains(plain, "—") {
		t.Fatalf("footer rendered while off: %q", plain)
	}
	if err := gw.store.SetChatSetting(t.Context(), "telegram", "600", "footer", "true", at.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if !gw.chatFlag(t.Context(), "telegram", "600", chatFooter) {
		t.Fatal("footer flag did not read back")
	}
	stream("r2")
	sends.waitN(t, "hello", 2)
	texts := sends.texts()
	cooked := texts[len(texts)-1]
	for _, want := range []string{"— openai/gpt-fake", "2 iter", "$0.001000", "0s"} {
		if !strings.Contains(cooked, want) {
			t.Fatalf("footer %q lacks %q", cooked, want)
		}
	}
}

// TestTimestampedReplyHasOnePrefix: GW-020 renders on the wire exactly once.
func TestTimestampedReplyHasOnePrefix(t *testing.T) {
	sends := newCommandSends(t)
	gw := newTestGateway(t, sends.url)
	at := time.Date(2026, 10, 30, 12, 34, 0, 0, time.UTC)
	gw.now = func() time.Time { return at }
	if err := gw.store.SetChatSetting(t.Context(), "telegram", "601", "timestamps", "true", at.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	ch := make(chan event.Update, 2)
	ch <- event.Update{Kind: event.UpdateTextDelta, Text: "hello"}
	ch <- event.Update{Kind: event.UpdateStop, Stop: event.StopEndTurn, Detail: event.DetailCompleted}
	close(ch)
	in := &telegram.Inbound{Platform: "telegram", ChatID: "601", SenderID: "7788", UpdateID: "u1", Text: "hi"}
	if err := gw.streamReply(t.Context(), in, ch, nil, nil); err != nil {
		t.Fatal(err)
	}
	text := sends.wait(t, "hello")
	if text != "[12:34] hello" {
		t.Fatalf("timestamped reply = %q", text)
	}
}
