package tui

import (
	"strings"
	"testing"
)

// TestSlashStatus is the SLASH-038 TUI acceptance: the notice names the
// session, model, ledger tokens, and the resolved context window.
func TestSlashStatus(t *testing.T) {
	t.Parallel()
	m := slashApp(t, &Config{Model: "fake/model"})
	m.sessionID = "sess-status"
	m.usage = Usage{Input: 1200, Output: 300}
	m.contextInfo = ContextInfo{Window: 200_000}
	runSlashText(t, m, "/status")
	got := lastNotice(m)
	for _, want := range []string{"status: session sess-status", "model fake/model", "tokens 1200/300", "context 1200/200000"} {
		if !strings.Contains(got, want) {
			t.Fatalf("notice = %q, want %q", got, want)
		}
	}
}

// TestSlashStatusUnknownWindow: an unresolved window is "?", never a made-up
// number.
func TestSlashStatusUnknownWindow(t *testing.T) {
	t.Parallel()
	m := slashApp(t, &Config{Model: "fake/model"})
	m.usage = Usage{Input: 10}
	runSlashText(t, m, "/status")
	if got := lastNotice(m); !strings.Contains(got, "context 10/?") {
		t.Fatalf("notice = %q", got)
	}
	if got := lastNotice(m); strings.Contains(got, "usage: /status") {
		t.Fatalf("unexpected args: %q", got)
	}
	runSlashText(t, m, "/status extra")
	if got := lastNotice(m); !strings.Contains(got, "usage: /status") {
		t.Fatalf("usage notice = %q", got)
	}
}
