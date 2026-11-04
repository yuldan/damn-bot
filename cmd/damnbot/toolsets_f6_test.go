package main

import (
	"runtime"
	"slices"
	"testing"

	"github.com/damnbot/damnbot/internal/modules"
	"github.com/damnbot/damnbot/internal/toolset"
)

// builtinToolset returns one built-in composition by name.
func builtinToolset(t *testing.T, name string) toolset.Definition {
	t.Helper()
	for _, def := range toolset.Builtin() {
		if def.Name == name {
			return def
		}
	}
	t.Fatalf("built-in toolset %q is missing", name)
	return toolset.Definition{}
}

// deferredToolNames is the set of tools the F6 module descriptors contribute
// to the deferred catalog: they are model-visible only after tool_search, never
// part of the eager registered tool list of a toolset.
func deferredToolNames(reg *modules.Registry) map[string]bool {
	out := make(map[string]bool)
	for _, desc := range reg.Descriptors() {
		for _, tool := range desc.Tools {
			out[tool.Name] = true
		}
	}
	return out
}

// TestF6TSET003BrowserCDP (TSET-003): the browser-cdp composition is exactly
// the two deferred tools browser_cdp and browser_dialog, and it resolves only
// when the deferred catalog exposes them (TSET inventaris=false).
func TestF6TSET003BrowserCDP(t *testing.T) {
	t.Parallel()
	def := builtinToolset(t, "browser-cdp")
	if want := []string{"browser_cdp", "browser_dialog"}; !slices.Equal(def.Tools, want) || len(def.Includes) != 0 {
		t.Fatalf("browser-cdp = %+v, want tools %v and no includes", def, want)
	}
	deferred := deferredToolNames(modules.Default())
	for _, name := range def.Tools {
		if !deferred[name] {
			t.Errorf("%s is not contributed by an F6 module descriptor", name)
		}
	}
	cfg := toolset.Config{Active: []string{"browser-cdp"}}
	available := map[string]bool{"browser_cdp": true, "browser_dialog": true}
	sel, err := toolset.Resolve(cfg, runtime.GOOS, available)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !slices.Equal(sel.Tools, def.Tools) || len(sel.Unavailable) != 0 {
		t.Fatalf("resolved = %+v", sel)
	}
	// Deferred inventory still hidden: the composition is valid but resolves
	// as unavailable instead of silently dropping a member.
	sel, err = toolset.Resolve(cfg, runtime.GOOS, map[string]bool{})
	if err != nil {
		t.Fatalf("resolve without inventory: %v", err)
	}
	if len(sel.Tools) != 0 || !slices.Equal(sel.Unavailable, def.Tools) {
		t.Fatalf("resolved without inventory = %+v", sel)
	}
}

// TestF6TSET004BrowserUse (TSET-004): the browser-use composition has exactly
// browser_exec, the one deferred tool that replaces the page tools when the
// backend is browser-use.
func TestF6TSET004BrowserUse(t *testing.T) {
	t.Parallel()
	def := builtinToolset(t, "browser-use")
	if want := []string{"browser_exec"}; !slices.Equal(def.Tools, want) || len(def.Includes) != 0 {
		t.Fatalf("browser-use = %+v, want tools %v and no includes", def, want)
	}
	if !deferredToolNames(modules.Default())["browser_exec"] {
		t.Error("browser_exec is not contributed by an F6 module descriptor")
	}
	cfg := toolset.Config{Active: []string{"browser-use"}}
	sel, err := toolset.Resolve(cfg, runtime.GOOS, map[string]bool{"browser_exec": true})
	if err != nil || !slices.Equal(sel.Tools, def.Tools) {
		t.Fatalf("resolved = %+v, %v", sel, err)
	}
	sel, err = toolset.Resolve(cfg, runtime.GOOS, map[string]bool{})
	if err != nil || len(sel.Tools) != 0 || !slices.Equal(sel.Unavailable, def.Tools) {
		t.Fatalf("resolved without inventory = %+v, %v", sel, err)
	}
}
