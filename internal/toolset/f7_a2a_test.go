package toolset

import (
	"slices"
	"testing"
)

// TestF7TSET033A2A is the a2a toolset: five tools, default-off, and every
// member resolves without a tool inventory.
func TestF7TSET033A2A(t *testing.T) {
	t.Parallel()
	want := []string{"a2a_call", "a2a_discover", "a2a_history", "a2a_list", "a2a_orchestrate"}
	def := builtinByName(t, "a2a")
	if !slices.Equal(def.Tools, want) {
		t.Fatalf("a2a tools = %v", def.Tools)
	}
	sel, err := Resolve(Config{Active: []string{"a2a"}}, "linux", available(want...))
	if err != nil || !slices.Equal(sel.Tools, want) || len(sel.Unavailable) != 0 {
		t.Fatalf("resolved = %+v, %v", sel, err)
	}
	// A binary that has not wired the client reports the members as
	// unavailable instead of dropping them silently.
	sel, err = Resolve(Config{Active: []string{"a2a"}}, "linux", available())
	if err != nil || len(sel.Tools) != 0 || !slices.Equal(sel.Unavailable, want) {
		t.Fatalf("absent backend = %+v, %v", sel, err)
	}
	for _, other := range Builtin() {
		if other.Name == "a2a" {
			continue
		}
		for _, tool := range want {
			if slices.Contains(resolveOne(builtinMap(), other.Name), tool) {
				t.Fatalf("%s pulls in %s; a2a must stay opt-in", other.Name, tool)
			}
		}
	}
}
