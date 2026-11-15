//go:build !minimal

package tui

import (
	"fmt"
	"strconv"
)

// statusSlash implements SLASH-038 from state the TUI already holds: session,
// model, ledger tokens of the turn, and the context window the runtime
// resolved. It performs no I/O and never claims a window it does not know.
func (m *Model) statusSlash(cmd slashCommand) {
	if len(cmd.args) != 0 {
		m.appendNotice("usage: /status")
		return
	}
	cfg := m.host.Config.Snapshot()
	limit := "?"
	if m.contextInfo.Window > 0 {
		limit = strconv.FormatInt(m.contextInfo.Window, 10)
	}
	m.appendNotice(fmt.Sprintf("status: session %s, model %s, tokens %d/%d, context %d/%s",
		m.sessionID, cfg.Model, m.usage.Input, m.usage.Output, m.usage.Input, limit))
}
