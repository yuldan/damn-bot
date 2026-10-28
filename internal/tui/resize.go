package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

type resizeSettledMsg struct{ seq uint64 }

// resize coalesces a SIGWINCH storm: only the latest size is committed after
// the 16 ms debounce (SPEC GAP-UX-05). The first size applies immediately so
// the startup frame is laid out without a timer.
func (m *Model) resize(msg tea.WindowSizeMsg) tea.Cmd {
	if m.width == 0 && m.height == 0 {
		m.pendingW, m.pendingH = msg.Width, msg.Height
		m.commitResize()
		return nil
	}
	m.pendingW, m.pendingH = msg.Width, msg.Height
	m.resizeSeq++
	seq := m.resizeSeq
	if m.resizeDebounce <= 0 {
		m.commitResize()
		return nil
	}
	return func() tea.Msg {
		<-time.After(m.resizeDebounce)
		return resizeSettledMsg{seq: seq}
	}
}

func (m *Model) commitResize() {
	if m.pendingW == m.width && m.pendingH == m.height {
		return
	}
	m.width, m.height = m.pendingW, m.pendingH
	m.resizeApplied++
}
