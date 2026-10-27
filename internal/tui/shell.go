package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type bangDoneMsg struct {
	res BangResult
	err error
}

// bangCmd runs one !cmd through Host.Shell (the tools dispatcher path); a
// denial or no transport never executes it.
func (m *Model) bangCmd(command string) tea.Cmd {
	command = strings.TrimSpace(command)
	if command == "" {
		m.appendNotice("usage: !<command>")
		return nil
	}
	if m.busy || m.bangBusy || m.approval != nil || m.confirm != nil || m.palette {
		m.appendNotice("bang refused: a turn or modal is active")
		return nil
	}
	m.bangBusy = true
	m.appendBlock(&Block{Kind: BlockUser, Text: "!" + command, At: m.host.Clock.Now()})
	shell, sid, plan := m.host.Shell, m.sessionID, m.plan
	return func() tea.Msg {
		res, err := shell.Bang(context.Background(), sid, command, plan)
		return bangDoneMsg{res: res, err: err}
	}
}
