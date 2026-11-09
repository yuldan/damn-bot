package resolve

import (
	"errors"
	"reflect"
	"testing"

	"github.com/damnbot/damnbot/internal/plugins/catalog"
)

func mustCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	return c
}

// firstOf returns the first entry of kind in ascending id order.
func firstOf(t *testing.T, c *catalog.Catalog, kind catalog.Kind) catalog.Entry {
	t.Helper()
	for _, e := range c.Entries() {
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("catalog has no %s entry", kind)
	return catalog.Entry{}
}

// TestF8PluginResolveTargets resolves one real catalog entry per consumption
// path against the registry set that serves it.
func TestF8PluginResolveTargets(t *testing.T) {
	t.Parallel()
	c := mustCatalog(t)
	targets := map[catalog.Kind]Target{}
	for _, kind := range []catalog.Kind{catalog.KindMCP, catalog.KindSkill, catalog.KindModule} {
		e := firstOf(t, c, kind)
		s := Sources{MCP: map[string][]string{}, Skills: map[string]struct{}{}, Modules: map[string]struct{}{}}
		switch kind {
		case catalog.KindMCP:
			s.MCP[e.ID] = []string{"ping"}
		case catalog.KindSkill:
			s.Skills[e.ID] = struct{}{}
		case catalog.KindModule:
			s.Modules[e.ID] = struct{}{}
		}
		got, err := Resolve(c, s, e.ID)
		if err != nil {
			t.Fatalf("%s %q: %v", kind, e.ID, err)
		}
		want := Target{Kind: kind, ID: e.ID}
		if got != want {
			t.Errorf("%s %q: Target = %+v, want %+v", kind, e.ID, got, want)
		}
		targets[kind] = got
	}
	if len(targets) != 3 {
		t.Fatalf("resolved kinds = %d, want all three", len(targets))
	}
}

// TestF8PluginResolveUnavailable proves a missing target is a typed status
// (module_unavailable), never a panic and never an implicit install: with
// empty registries all 645 entries are unavailable.
func TestF8PluginResolveUnavailable(t *testing.T) {
	t.Parallel()
	c := mustCatalog(t)
	empty := Sources{}
	for _, e := range c.Entries() {
		got, err := Resolve(c, empty, e.ID)
		if !errors.Is(err, ErrModuleUnavailable) {
			t.Fatalf("%s: err = %v, want ErrModuleUnavailable", e.ID, err)
		}
		if got != (Target{}) {
			t.Fatalf("%s: Target = %+v, want zero", e.ID, got)
		}
	}
	// A registry of foreign ids does not make the catalog entry appear.
	e := firstOf(t, c, catalog.KindMCP)
	foreign := Sources{MCP: map[string][]string{"foreign-server": {"ping"}}}
	if _, err := Resolve(c, foreign, e.ID); !errors.Is(err, ErrModuleUnavailable) {
		t.Fatalf("foreign-only registry: err = %v, want ErrModuleUnavailable", err)
	}
}

// TestF8PluginResolveForeign rejects an id outside the closed catalog even
// when the live registries happen to contain that name.
func TestF8PluginResolveForeign(t *testing.T) {
	t.Parallel()
	c := mustCatalog(t)
	s := Sources{
		MCP:     map[string][]string{"foreign-server": {"ping"}},
		Skills:  map[string]struct{}{"foreign-skill": {}},
		Modules: map[string]struct{}{"foreign-module": {}},
	}
	for _, id := range []string{"foreign-server", "foreign-skill", "foreign-module", ""} {
		got, err := Resolve(c, s, id)
		if !errors.Is(err, catalog.ErrUnknown) {
			t.Fatalf("Resolve(%q) = (%+v, %v), want catalog.ErrUnknown", id, got, err)
		}
	}
}

// TestF8PluginResolveNoExecution proves resolution is read-only: it never
// mutates the caller's registries and leaves no file behind in the workspace.
func TestF8PluginResolveNoExecution(t *testing.T) {
	t.Parallel()
	c := mustCatalog(t)
	s := Sources{
		MCP:     map[string][]string{"0bull": {"ping", "tap"}},
		Skills:  map[string]struct{}{"pstack": {}},
		Modules: map[string]struct{}{"web": {}},
	}
	before := Sources{
		MCP:     map[string][]string{"0bull": {"ping", "tap"}},
		Skills:  map[string]struct{}{"pstack": {}},
		Modules: map[string]struct{}{"web": {}},
	}
	for _, e := range c.Entries() {
		if _, err := Resolve(c, s, e.ID); err != nil && !errors.Is(err, ErrModuleUnavailable) {
			t.Fatalf("%s: unexpected error %v", e.ID, err)
		}
	}
	if !reflect.DeepEqual(s, before) {
		t.Fatalf("Resolve mutated Sources: %+v", s)
	}
}
