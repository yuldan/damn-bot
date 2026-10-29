package tui

import tea "charm.land/bubbletea/v2"

// Non-critical notice content. There is exactly one tip in F3; adding another
// key is a product decision, and the gate guarantees at most one showing per
// key (GAP-OPS-09). Content must stay informational: no upsell, promo, or
// sign-in prompt (ADR-022 NP-19).
const (
	startupTipKey  = "tip:palette"
	startupTipText = "tip: ctrl+p opens the command palette"
)

// startupNoticeMsg is delivered asynchronously by [Model.Init]; it is never a
// modal and never blocks input.
type startupNoticeMsg struct{ text string }

// Init asks the notice gate off the UI goroutine and keeps P12 intact: no I/O
// happens before the first frame.
func (m *Model) Init() tea.Cmd {
	gate := m.host.Notices
	if gate == nil {
		return nil
	}
	return func() tea.Msg {
		ok, err := gate.Allow(startupTipKey)
		if err != nil || !ok {
			return nil
		}
		return startupNoticeMsg{text: startupTipText}
	}
}
