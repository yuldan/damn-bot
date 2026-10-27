package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

func altDigit(msg tea.KeyPressMsg) (int, bool) {
	if msg.Mod != tea.ModAlt || msg.Code < '1' || msg.Code > '9' {
		return 0, false
	}
	return int(msg.Code - '0'), true
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (m *Model) tabBar() string {
	parts := make([]string, 0, min(len(m.tabs), 9))
	for i := range min(len(m.tabs), 9) {
		s := m.tabs[i]
		mark, ro := "", ""
		if s.ID == m.sessionID {
			mark = "*"
		}
		if s.ReadOnly {
			ro = " ro"
		}
		parts = append(parts, fmt.Sprintf("[%d %s%s%s]", i+1, shortID(s.ID), ro, mark))
	}
	return strings.Join(parts, " ")
}

// selectTab uses the loaded tab list, loading ctxdb sessions on first use.
func (m *Model) selectTab(idx int) tea.Cmd {
	if idx < 0 {
		return nil
	}
	if idx >= len(m.tabs) {
		if !m.tabsLoaded {
			m.pendingTab = idx + 1
			return m.loadSessions()
		}
		m.appendNotice(fmt.Sprintf("no session tab %d", idx+1))
		return nil
	}
	return m.activateTab(idx)
}

func (m *Model) activateTab(idx int) tea.Cmd {
	s := m.tabs[idx]
	if s.ID == m.sessionID {
		m.appendNotice("already on session " + s.ID)
		return nil
	}
	if m.busy {
		m.appendNotice("tab switch refused: a turn is running; cancel it first")
		return nil
	}
	m.switchSession(s.ID)
	m.appendNotice(fmt.Sprintf("tab %d: %s", idx+1, s.ID))
	return nil
}

// ensureTab keeps the active session present in the tab bar.
func (m *Model) ensureTab(id string) {
	for i := range m.tabs {
		if m.tabs[i].ID == id {
			return
		}
	}
	m.tabs = append([]SessionInfo{{ID: id}}, m.tabs...)
}
