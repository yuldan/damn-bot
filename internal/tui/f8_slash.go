package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// f8Slash runs one F8 slash command through the injected seam. A nil seam is
// a typed module_unavailable notice; the command never becomes a model turn.
func (m *Model) f8Slash(cmd slashCommand) tea.Cmd {
	if m.host.F8 == nil {
		m.appendNotice("module_unavailable: " + cmd.name + " is not wired in this build")
		return nil
	}
	reply := strings.TrimSpace(m.host.F8.F8TUI(context.Background(), m.sessionID, cmd.name, cmd.args))
	if reply == "" {
		reply = cmd.name + ": ok"
	}
	m.appendNotice(reply)
	return nil
}
