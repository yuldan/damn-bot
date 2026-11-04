package main

import (
	"runtime"
	"slices"
	"testing"

	"github.com/damnbot/damnbot/internal/modules"
	"github.com/damnbot/damnbot/internal/toolset"
)

// browserToolNames is the TSET-005 composition: the eleven managed page tools
// of the parity browser toolset.
func browserToolNames() []string {
	return []string{
		"browser_navigate", "browser_snapshot", "browser_click", "browser_type",
		"browser_scroll", "browser_back", "browser_press", "browser_get_images",
		"browser_vision", "browser_console", "browser_vault",
	}
}

// TestF6TSET005Browser (TSET-005): the `browser` composition is exactly the
// eleven managed page tools, without duplicates, and every member is a
// deferred F6 module tool (visible only through tool_search, never eagerly).
func TestF6TSET005Browser(t *testing.T) {
	t.Parallel()
	want := browserToolNames()
	def := builtinToolset(t, "browser")
	if !slices.Equal(def.Tools, want) || len(def.Includes) != 0 {
		t.Fatalf("browser = %+v, want tools %v and no includes", def, want)
	}
	assertNoDuplicates(t, def.Tools)
	assertDeferredTools(t, def.Tools)
	assertBrowserResolution(t, want, true)
	assertBrowserResolution(t, want, false)
}

func assertNoDuplicates(t *testing.T, names []string) {
	t.Helper()
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			t.Fatalf("%s is listed twice", name)
		}
		seen[name] = true
	}
}

func assertDeferredTools(t *testing.T, names []string) {
	t.Helper()
	deferred := deferredToolNames(modules.Default())
	for _, name := range names {
		if !deferred[name] {
			t.Errorf("%s is not contributed by an F6 module descriptor", name)
		}
	}
}

// assertBrowserResolution checks both inventory states: with the deferred
// catalog available every member resolves; without it the composition is
// reported unavailable instead of silently dropping a member.
func assertBrowserResolution(t *testing.T, want []string, inventory bool) {
	t.Helper()
	available := make(map[string]bool, len(want))
	if inventory {
		for _, name := range want {
			available[name] = true
		}
	}
	sel, err := toolset.Resolve(toolset.Config{Active: []string{"browser"}}, runtime.GOOS, available)
	if err != nil {
		t.Fatalf("resolve (inventory=%v): %v", inventory, err)
	}
	if inventory {
		if len(sel.Tools) != len(want) || len(sel.Unavailable) != 0 {
			t.Fatalf("resolved = %+v", sel)
		}
		for _, name := range want {
			if !slices.Contains(sel.Tools, name) {
				t.Fatalf("resolved = %+v, missing %s", sel, name)
			}
		}
		return
	}
	if len(sel.Tools) != 0 || len(sel.Unavailable) != len(want) {
		t.Fatalf("resolved without inventory = %+v", sel)
	}
	for _, name := range want {
		if !slices.Contains(sel.Unavailable, name) {
			t.Fatalf("resolved without inventory = %+v, missing %s", sel, name)
		}
	}
}
