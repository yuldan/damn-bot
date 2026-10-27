package tui

import (
	"context"
	"fmt"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// runSlash dispatches a command; a refusal is a notice, never a fake success.
func (m *Model) runSlash(cmd slashCommand) tea.Cmd {
	sessions, sid, ctx := m.host.Sessions, m.sessionID, context.Background()
	if cmd.kind == slashPlan {
		if len(cmd.args) != 0 {
			m.appendNotice("usage: /plan")
			return nil
		}
		m.plan = !m.plan
		state := "off"
		if m.plan {
			state = "on"
		}
		m.appendNotice("plan mode " + state + " for the next turn")
		return nil
	}
	if cmd.kind == slashUnknown {
		m.appendNotice("unknown command " + cmd.name + "; available: /new /fork /rewind /undo /plan")
		return nil
	}
	want := 0
	if cmd.kind == slashRewind {
		want = 1
	}
	if len(cmd.args) != want {
		if want == 0 {
			m.appendNotice("usage: " + cmd.name)
		} else {
			m.appendNotice("usage: /rewind <seq|turn:ID>")
		}
		return nil
	}
	if m.busy {
		m.appendNotice(cmd.name + " refused: a turn is running; cancel it first")
		return nil
	}
	switch cmd.kind {
	case slashNew:
		return func() tea.Msg {
			change, err := sessions.New(ctx)
			return sessionDoneMsg{op: "new", change: change, err: err}
		}
	case slashFork:
		return func() tea.Msg {
			change, err := sessions.Fork(ctx, sid)
			return sessionDoneMsg{op: "fork", change: change, err: err}
		}
	case slashRewind:
		at := cmd.args[0]
		return func() tea.Msg {
			change, err := sessions.Rewind(ctx, sid, at)
			return sessionDoneMsg{op: "rewind", change: change, err: err}
		}
	case slashUndo:
		return m.undoCmd("")
	case slashPlan, slashUnknown:
		return nil
	}
	return nil
}

func (m *Model) undoCmd(target string) tea.Cmd {
	sessions, sid := m.host.Sessions, m.sessionID
	return func() tea.Msg {
		detail, err := sessions.Undo(context.Background(), sid, target)
		return undoDoneMsg{detail: detail, err: err}
	}
}

// applySessionDone installs the new session and reports the real outcome.
func (m *Model) applySessionDone(msg sessionDoneMsg) {
	if msg.err != nil {
		m.appendNotice(msg.op + " refused: " + msg.err.Error())
		return
	}
	if msg.change.SessionID != "" {
		m.switchSession(msg.change.SessionID)
	}
	if msg.change.Detail != "" {
		m.appendNotice(msg.change.Detail)
	}
}

// switchSession resets the transcript and turn accounting for a new branch.
func (m *Model) switchSession(id string) {
	m.sessionID = id
	m.blocks = nil
	m.usage = Usage{}
	m.stop, m.detail = "", ""
	m.settledCost, m.settledKnown, m.turnSettled = 0, false, true
	m.fallback, m.plan = false, false
	m.scroll, m.navMode, m.navIndex, m.collapsed = 0, false, 0, nil
}

// appendNotice adds one system notice stamped by the injected clock.
func (m *Model) appendNotice(text string) {
	m.appendBlock(&Block{Kind: BlockNotice, Text: text, At: m.host.Clock.Now()})
}

// enterNav selects the newest block. An empty transcript keeps the mode off.
func (m *Model) enterNav() {
	if len(m.blocks) == 0 {
		m.appendNotice("no blocks to navigate")
		return
	}
	m.navMode, m.navIndex, m.scroll = true, len(m.blocks)-1, 0
	if m.collapsed == nil {
		m.collapsed = map[int]bool{}
	}
}

// exitNav returns to the prompt and follows the tail again.
func (m *Model) exitNav() {
	m.navMode, m.navIndex, m.scroll, m.collapsed = false, 0, 0, nil
}

// handleNavKey is the ctrl+k keymap; every key shown in the footer works.
func (m *Model) handleNavKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Nav), key.Matches(msg, m.keys.Cancel):
		m.exitNav()
	case key.Matches(msg, m.keys.NavUp):
		m.selectBlock(-1)
	case key.Matches(msg, m.keys.NavDown):
		m.selectBlock(1)
	case key.Matches(msg, m.keys.NavFold):
		m.collapsed[m.navIndex] = !m.collapsed[m.navIndex]
		if !m.collapsed[m.navIndex] {
			delete(m.collapsed, m.navIndex)
		}
	case key.Matches(msg, m.keys.NavCopy):
		return m.copySelected()
	case key.Matches(msg, m.keys.NavBlob):
		m.showBlob()
	case key.Matches(msg, m.keys.NavFork):
		m.appendNotice("fork per block is unavailable: the loop stream carries no turn/seq per block; use /fork to fork at the head")
	case key.Matches(msg, m.keys.NavUndo):
		return m.undoSelected()
	}
	return nil
}

// selectBlock moves the selection, which stays the newest block in the window.
func (m *Model) selectBlock(delta int) {
	m.navIndex = max(0, min(len(m.blocks)-1, m.navIndex+delta))
	m.scroll = max(0, len(m.blocks)-1-m.navIndex)
}

// copySelected copies plain content (tool blocks include the spill path).
func (m *Model) copySelected() tea.Cmd {
	text := m.blockCopyText(m.navIndex)
	if text == "" {
		m.appendNotice("selected block has no text to copy")
		return nil
	}
	clip := m.host.Clipboard
	return func() tea.Msg {
		if err := clip.Copy(text); err != nil {
			return copyDoneMsg{err: err}
		}
		return copyDoneMsg{chars: utf8.RuneCountInString(text)}
	}
}

func (m *Model) blockCopyText(i int) string {
	if i < 0 || i >= len(m.blocks) {
		return ""
	}
	if b := &m.blocks[i]; b.Kind == BlockTool {
		return renderToolBlock(b)
	}
	return m.blocks[i].Text
}

// showBlob prints the spill path without opening an external editor.
func (m *Model) showBlob() {
	b := m.blocks[m.navIndex]
	switch {
	case b.Spill == "":
		m.appendNotice("selected block has no spill blob")
	case b.SpillSize > 0:
		m.appendNotice(fmt.Sprintf("blob %s (%s chars)", b.Spill, formatSize(b.SpillSize)))
	default:
		m.appendNotice("blob " + b.Spill)
	}
}

// undoSelected undoes the real journal action of the selected tool block.
func (m *Model) undoSelected() tea.Cmd {
	b := m.blocks[m.navIndex]
	if m.busy {
		m.appendNotice("undo refused: a turn is running; cancel it first")
		return nil
	}
	if b.Kind != BlockTool || b.ActionID == 0 {
		m.appendNotice("selected block has no undoable action")
		return nil
	}
	return m.undoCmd(fmt.Sprintf("a%d", b.ActionID))
}
