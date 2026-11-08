package toolset

import "testing"

// TestF7TSET033A2A is the a2a toolset: five tools, default-off, and every
// member resolves without a tool inventory.
func TestF7TSET033A2A(t *testing.T) {
	t.Parallel()
	assertOptionalToolset(t, "a2a", []string{
		"a2a_call", "a2a_discover", "a2a_history", "a2a_list", "a2a_orchestrate",
	})
}
