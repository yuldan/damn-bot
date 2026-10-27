package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

type planStep struct{ ID, Text, Status string }

// planSteps parses the newest todo result ("- [ ] id: text"); there is no
// second plan store.
func planSteps(blocks []Block) []planStep {
	for i := len(blocks) - 1; i >= 0; i-- {
		b := &blocks[i]
		if b.Kind != BlockTool || b.ToolName != "todo" {
			continue
		}
		var out []planStep
		for line := range strings.SplitSeq(b.Text, "\n") {
			line = strings.TrimSpace(line)
			if len(line) < 8 || !strings.HasPrefix(line, "- [") || line[4] != ']' {
				continue
			}
			id, text, ok := strings.Cut(strings.TrimSpace(line[5:]), ": ")
			if !ok {
				continue
			}
			status := map[byte]string{' ': "pending", '~': "in_progress", 'x': "done"}[line[3]]
			if status == "" {
				status = "unknown"
			}
			out = append(out, planStep{ID: id, Text: text, Status: status})
		}
		return out
	}
	return nil
}

type planPanel struct{}

func newPlanPanel() panel { return planPanel{} }

func (planPanel) title() string { return routePlan }

func (planPanel) body(m *Model) string {
	steps := planSteps(m.blocks)
	if len(steps) == 0 {
		return m.styles.hint.Render("no plan yet (run the todo tool)")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "plan · %d steps\n", len(steps))
	deviation := false
	for _, s := range steps {
		mark := map[string]string{"pending": " ", "in_progress": "~", "done": "x"}[s.Status]
		if mark == "" {
			deviation = true
			fmt.Fprintf(&b, "! deviation: %s: %s\n", s.ID, s.Text)
			continue
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", mark, s.ID, s.Text)
	}
	if !deviation {
		b.WriteString("\ndeviation: none reported (plan_deviation is not emitted yet)")
	}
	return b.String()
}

type sessionsLoadedMsg struct {
	items []SessionInfo
	err   error
}

type sessionsPanel struct{}

func newSessionsPanel() panel { return sessionsPanel{} }

func (sessionsPanel) title() string { return routeSessions }

func (sessionsPanel) body(m *Model) string {
	switch {
	case m.sessionErr != nil:
		return m.styles.hint.Render("sessions unavailable: " + m.sessionErr.Error())
	case len(m.sessionItems) == 0:
		return m.styles.hint.Render("no sessions (still loading or empty)")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "sessions · %d\n", len(m.sessionItems))
	for i, s := range m.sessionItems {
		mark, current := " ", " "
		if i == m.sessionSel {
			mark = "▸"
		}
		if s.ID == m.sessionID {
			current = "*"
		}
		fmt.Fprintf(&b, "%s%s %s · %s · %s\n", mark, current, s.ID, s.Model, s.Cwd)
	}
	b.WriteString("j/k select · enter switch · esc chat")
	return b.String()
}

func (m *Model) loadSessions() tea.Cmd {
	sessions := m.host.Sessions
	return func() tea.Msg {
		items, err := sessions.List(context.Background())
		return sessionsLoadedMsg{items: items, err: err}
	}
}

func (m *Model) applySessions(msg sessionsLoadedMsg) {
	m.sessionItems, m.sessionErr, m.sessionSel = msg.items, msg.err, 0
	for i, s := range msg.items {
		if s.ID == m.sessionID {
			m.sessionSel = i
		}
	}
}

// handleSessionsKey intercepts selection keys only while the panel is active.
func (m *Model) handleSessionsKey(msg tea.KeyPressMsg) bool {
	switch {
	case key.Matches(msg, m.keys.NavUp):
		m.sessionSel = max(0, m.sessionSel-1)
	case key.Matches(msg, m.keys.NavDown):
		if len(m.sessionItems) > 0 {
			m.sessionSel = min(len(m.sessionItems)-1, m.sessionSel+1)
		}
	case key.Matches(msg, m.keys.NavFold):
		if len(m.sessionItems) == 0 {
			return true
		}
		id := m.sessionItems[m.sessionSel].ID
		if m.busy {
			m.appendNotice("switch refused: a turn is running; cancel it first")
			return true
		}
		if id == m.sessionID {
			m.appendNotice("already on session " + id)
			return true
		}
		m.switchSession(id)
		m.setRoute(routes()[0])
		m.appendNotice("switched to session " + id)
	case key.Matches(msg, m.keys.Cancel):
		m.setRoute(routes()[0])
	default:
		return false
	}
	return true
}
