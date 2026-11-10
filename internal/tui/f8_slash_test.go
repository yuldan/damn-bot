package tui

import "testing"

// TestF8TUISlashParse proves the F8 slash aliases are in the TUI catalog and
// parse to the F8 kind (the gateway seam tests the behaviour).
func TestF8TUISlashParse(t *testing.T) {
	for _, name := range []string{"/goal", "/suggestions", "/suggest", "/blueprint", "/bp", "/journey", "/learning", "/memory-graph", "/pet"} {
		cmd, ok := parseSlash(name + " x")
		if !ok || cmd.kind != slashF8 {
			t.Fatalf("%s parsed kind=%v ok=%v", name, cmd.kind, ok)
		}
		if cmd.name != name {
			t.Fatalf("%s name=%q", name, cmd.name)
		}
	}
}
