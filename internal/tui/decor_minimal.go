//go:build minimal

package tui

// decorParts keeps the minimal build free of the mascot and persona stores.
func (m *Model) decorParts() []string { return nil }
