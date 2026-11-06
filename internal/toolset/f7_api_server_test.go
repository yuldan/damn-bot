package toolset

import (
	"runtime"
	"slices"
	"testing"
)

// TestF7TSET042APIServerBundle proves the api-server bundle is a composite
// without its own tool registry, and that it never widens the safe set.
func TestF7TSET042APIServerBundle(t *testing.T) {
	t.Parallel()
	def, ok := lookupDef("hermes-api-server")
	if !ok {
		t.Fatal("hermes-api-server is not a built-in toolset")
	}
	core, ok := lookupDef("hermes-cli")
	if !ok {
		t.Fatal("hermes-cli missing")
	}
	want := slices.DeleteFunc(slices.Clone(core.Tools), func(t string) bool { return t == "clarify" })
	if !slices.Equal(def.Tools, want) {
		t.Errorf("tools = %v, want hermes-cli minus clarify %v", def.Tools, want)
	}
	if len(def.Includes) != 0 {
		t.Errorf("includes = %v, want none", def.Includes)
	}
	declared := DeclaredTools()
	for _, name := range def.Tools {
		if !declared[name] {
			t.Errorf("tool %q is not declared by another built-in (own registry)", name)
		}
	}
	if len(slices.Compact(slices.Clone(def.Tools))) != len(def.Tools) {
		t.Error("duplicate tool in bundle")
	}

	checkResolveAll(t, &def)
	checkResolvePartial(t, &def)
}

func checkResolvePartial(t *testing.T, def *Definition) {
	t.Helper()
	partial := map[string]bool{}
	for _, name := range def.Tools[:2] {
		partial[name] = true
	}
	sel, err := Resolve(Config{Active: []string{"hermes-api-server"}}, runtime.GOOS, partial)
	if err != nil {
		t.Fatalf("Resolve partial: %v", err)
	}
	if len(sel.Unavailable) != len(def.Tools)-2 {
		t.Errorf("partial unavailable = %d, want %d", len(sel.Unavailable), len(def.Tools)-2)
	}
}

func lookupDef(name string) (Definition, bool) {
	for _, d := range Builtin() {
		if d.Name == name {
			return d, true
		}
	}
	return Definition{}, false
}

func checkResolveAll(t *testing.T, def *Definition) {
	t.Helper()
	sel, err := Resolve(Config{Active: []string{"hermes-api-server"}}, runtime.GOOS, DeclaredTools())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(sel.Unavailable) != 0 || len(sel.Allowed) != len(def.Tools) {
		t.Errorf("unavailable = %v allowed = %d", sel.Unavailable, len(sel.Allowed))
	}
	for _, name := range def.Tools {
		if !sel.Allowed[name] {
			t.Errorf("tool %q not exposed", name)
		}
	}
}
