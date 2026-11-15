//go:build minimal

package tui

// statusSlash is a no-op in the minimal build; the per-session report is
// excluded from the size-gated binary (P13).
func (m *Model) statusSlash(slashCommand) {}
