package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func tabKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyTab} }

// TestPlanView renders the newest todo result with per-step status and flags
// an unknown status as a deviation; no second plan source is invented.
func TestPlanView(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestApp(t)
	m.appendBlock(&Block{Kind: BlockTool, ToolName: "todo", Text: strings.Join([]string{
		"4 items: 1 pending, 1 in progress, 1 done",
		"- [ ] a: first",
		"- [~] b: second",
		"- [x] c: third",
		"- [?] d: weird",
	}, "\n")})
	m.Update(tabKey()) // chat -> plan
	view := m.View().Content
	for _, want := range []string{"plan · 4 steps", "[ ] a: first", "[~] b: second", "[x] c: third", "! deviation: d: weird"} {
		if !strings.Contains(view, want) {
			t.Fatalf("plan view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "deviation: none reported") {
		t.Fatalf("plan view claims no deviation while one is shown")
	}
}

func TestPlanViewEmpty(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestApp(t)
	m.Update(tabKey())
	if !strings.Contains(m.View().Content, "no plan yet") {
		t.Fatalf("empty plan view has no hint:\n%s", m.View().Content)
	}
}

// TestSessionsViewSwitch lists real-shaped ctxdb rows, selects one, and
// switches the active session.
func TestSessionsViewSwitch(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestApp(t)
	sessions, ok := m.host.Sessions.(*fakeSessions)
	if !ok {
		t.Fatalf("sessions = %T", m.host.Sessions)
	}
	sessions.items = []SessionInfo{
		{ID: "sess-2", Cwd: "/w2", Model: "fake/model"},
		{ID: "sess-1", Cwd: "/w1", Model: "fake/model"},
	}
	m.Update(tabKey()) // chat -> plan
	_, cmd := m.Update(tabKey())
	if cmd == nil {
		t.Fatalf("sessions tab returned no load command")
	}
	m.Update(cmd())
	view := m.View().Content
	for _, want := range []string{"sessions · 2", "sess-2", "sess-1", "▸*"} {
		if !strings.Contains(view, want) {
			t.Fatalf("sessions view missing %q:\n%s", want, view)
		}
	}
	m.Update(navKey('k', 0)) // current sess-1 -> sess-2
	m.Update(navKey(tea.KeyEnter, 0))
	if m.sessionID != "sess-2" {
		t.Fatalf("session = %q, want sess-2", m.sessionID)
	}
	if m.Route() != routeChat {
		t.Fatalf("route = %q after switching, want chat", m.Route())
	}
	if !strings.Contains(m.View().Content, "switched to session sess-2") {
		t.Fatalf("switch notice missing")
	}
}

func TestSessionsViewRefusals(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestApp(t)
	sessions, ok := m.host.Sessions.(*fakeSessions)
	if !ok {
		t.Fatalf("sessions = %T", m.host.Sessions)
	}
	sessions.items = []SessionInfo{{ID: "sess-2"}}
	m.Update(tabKey())
	_, cmd := m.Update(tabKey())
	m.Update(cmd())
	m.busy = true
	m.Update(navKey(tea.KeyEnter, 0))
	last := m.blocks[len(m.blocks)-1]
	if m.sessionID != "sess-1" || !strings.Contains(last.Text, "switch refused: a turn is running") {
		t.Fatalf("busy switch was not refused: %+v", last)
	}

	other, _, _ := newTestApp(t)
	others, ok := other.host.Sessions.(*fakeSessions)
	if !ok {
		t.Fatalf("sessions = %T", other.host.Sessions)
	}
	others.listErr = errors.New("db busy")
	other.Update(tabKey())
	_, cmd = other.Update(tabKey())
	other.Update(cmd())
	if !strings.Contains(other.View().Content, "sessions unavailable: db busy") {
		t.Fatalf("list error hidden:\n%s", other.View().Content)
	}
}
