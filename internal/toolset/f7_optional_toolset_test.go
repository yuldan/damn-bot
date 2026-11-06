package toolset

import (
	"slices"
	"testing"
)

// assertOptionalToolset checks one default-off toolset: exact membership, a
// clean resolve when the binary registers every member, unavailable members
// (never silent drops) when it registers none, and no other bundle pulling a
// member in.
func assertOptionalToolset(t *testing.T, name string, want []string) {
	t.Helper()
	def := builtinByName(t, name)
	if !slices.Equal(def.Tools, want) {
		t.Fatalf("%s tools = %v", name, def.Tools)
	}
	sel, err := Resolve(Config{Active: []string{name}}, "linux", available(want...))
	if err != nil || !slices.Equal(sel.Tools, want) || len(sel.Unavailable) != 0 {
		t.Fatalf("resolved = %+v, %v", sel, err)
	}
	sel, err = Resolve(Config{Active: []string{name}}, "linux", available())
	if err != nil || len(sel.Tools) != 0 || !slices.Equal(sel.Unavailable, want) {
		t.Fatalf("absent backend = %+v, %v", sel, err)
	}
	for _, other := range Builtin() {
		if other.Name == name {
			continue
		}
		for _, tool := range want {
			if slices.Contains(resolveOne(builtinMap(), other.Name), tool) {
				t.Fatalf("%s pulls in %s; %s must stay opt-in", other.Name, tool, name)
			}
		}
	}
}
