package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func altKey(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Mod: tea.ModAlt} }

func TestTabsRenderAndSwitch(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestApp(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if !strings.Contains(m.View().Content, "[1 sess-1*]") {
		t.Fatalf("tab bar missing active marker:\n%s", m.View().Content)
	}
	sessions, ok := m.host.Sessions.(*fakeSessions)
	if !ok {
		t.Fatalf("sessions = %T", m.host.Sessions)
	}
	sessions.items = []SessionInfo{
		{ID: "sess-2", Cwd: "/w2", Model: "fake/model", ReadOnly: true},
		{ID: "sess-1", Cwd: "/w1", Model: "fake/model"},
	}
	cmd := m.handleKey(altKey('2'))
	if cmd == nil {
		t.Fatalf("alt+2 returned no load command")
	}
	m.Update(cmd())
	view := m.View().Content
	if !strings.Contains(view, "[1 sess-2 ro]") || !strings.Contains(view, "sess-1*") {
		t.Fatalf("tab bar after load:\n%s", view)
	}
	m.Update(altKey('1'))
	if m.sessionID != "sess-2" {
		t.Fatalf("session = %q, want sess-2", m.sessionID)
	}
	if !strings.Contains(m.View().Content, "sess-2 ro*") {
		t.Fatalf("active marker not moved:\n%s", m.View().Content)
	}
}

func TestTabsRefusals(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestApp(t)
	sessions, ok := m.host.Sessions.(*fakeSessions)
	if !ok {
		t.Fatalf("sessions = %T", m.host.Sessions)
	}
	sessions.items = []SessionInfo{{ID: "sess-1"}, {ID: "sess-2"}}
	m.Update(m.handleKey(altKey('2'))())
	m.Update(altKey('1')) // back to sess-1
	m.busy = true
	m.Update(altKey('2')) // sess-2 while busy
	if m.sessionID != "sess-1" || !strings.Contains(lastBlockText(m), "tab switch refused") {
		t.Fatalf("busy tab switch not refused: %q", lastBlockText(m))
	}
	m.busy = false
	m.Update(altKey('9'))
	if !strings.Contains(lastBlockText(m), "no session tab 9") {
		t.Fatalf("missing tab not reported: %q", lastBlockText(m))
	}
}

func lastBlockText(m *Model) string {
	if len(m.blocks) == 0 {
		return ""
	}
	return m.blocks[len(m.blocks)-1].Text
}
