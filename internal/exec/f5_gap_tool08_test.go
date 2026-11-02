//go:build !minimal

package exec

import (
	"fmt"
	"testing"
	"time"
)

// TestProcessNotifyRateLimit is the GAP-TOOL-08 gate: one notice per 15 s per
// process, four per minute per chat, fifteen per ten seconds globally; excess
// notices are merged (+N), not silently dropped.
func TestProcessNotifyRateLimit(t *testing.T) {
	t.Parallel()
	t.Run("process-cooldown-and-merge", func(t *testing.T) { t.Parallel(); processCooldown(t) })
	t.Run("chat-minute-cap", func(t *testing.T) { t.Parallel(); chatCap(t) })
	t.Run("global-window-cap", func(t *testing.T) { t.Parallel(); globalCap(t) })
	t.Run("global-refills", func(t *testing.T) { t.Parallel(); globalRefill(t) })
}

func processCooldown(t *testing.T) {
	t.Helper()
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	l := NewNoticeLimiter()
	if send, merged := l.Offer("p1", "chat", base); !send || merged != 0 {
		t.Fatalf("first = %v/%d", send, merged)
	}
	for i := range 100 {
		if send, merged := l.Offer("p1", "chat", base.Add(time.Duration(i)*time.Millisecond)); send || merged != 0 {
			t.Fatalf("burst %d = %v/%d, want suppressed", i, send, merged)
		}
	}
	send, merged := l.Offer("p1", "chat", base.Add(NoticeProcessEvery))
	if !send || merged != 100 {
		t.Fatalf("after cooldown = %v/%d, want merged 100", send, merged)
	}
}

func chatCap(t *testing.T) {
	t.Helper()
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	l := NewNoticeLimiter()
	sent := 0
	for i := range 10 {
		if send, _ := l.Offer(fmt.Sprintf("p%d", i), "same-chat", base); send {
			sent++
		}
	}
	if sent != NoticeChatLimit {
		t.Fatalf("chat sends = %d, want %d", sent, NoticeChatLimit)
	}
}

func globalCap(t *testing.T) {
	t.Helper()
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	l := NewNoticeLimiter()
	sent := 0
	for i := range 20 {
		if send, _ := l.Offer(fmt.Sprintf("p%d", i), fmt.Sprintf("chat%d", i), base); send {
			sent++
		}
	}
	if sent != NoticeGlobalLimit {
		t.Fatalf("global sends = %d, want %d", sent, NoticeGlobalLimit)
	}
}

func globalRefill(t *testing.T) {
	t.Helper()
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	l := NewNoticeLimiter()
	for i := range NoticeGlobalLimit {
		l.Offer(fmt.Sprintf("p%d", i), fmt.Sprintf("c%d", i), base)
	}
	if send, _ := l.Offer("new", "new-chat", base.Add(NoticeGlobalWindow)); !send {
		t.Fatal("global bucket did not refill after its window")
	}
}
