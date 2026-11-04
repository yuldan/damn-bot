package toolset

import (
	"errors"
	"slices"
	"testing"
)

func webBuiltin(t *testing.T, name string) Definition {
	t.Helper()
	for _, d := range Builtin() {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("built-in toolset %q is missing", name)
	return Definition{}
}

// TestF6TSET030Web: toolset `web` exposes exactly the two web tools and an
// inventory member that is not registered resolves as unavailable, never as
// silently dropped or auto-approved.
func TestF6TSET030Web(t *testing.T) {
	t.Parallel()
	def := webBuiltin(t, "web")
	if want := []string{"web_search", "web_fetch"}; !slices.Equal(def.Tools, want) {
		t.Fatalf("web tools = %v, want %v", def.Tools, want)
	}
	sel, err := Resolve(Config{Active: []string{"web"}}, "linux", available("web_fetch", "web_search", "shell"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := []string{"web_fetch", "web_search"}; !slices.Equal(sel.Tools, want) {
		t.Fatalf("inventory tools = %v, want %v", sel.Tools, want)
	}
	if len(sel.Unavailable) != 0 {
		t.Fatalf("inventory unavailable = %v", sel.Unavailable)
	}
	for _, name := range sel.Tools {
		if !sel.Allowed[name] {
			t.Errorf("%s exposed but not allowed", name)
		}
	}

	partial, err := Resolve(Config{Active: []string{"web"}}, "linux", available("web_search"))
	if err != nil {
		t.Fatalf("Resolve partial: %v", err)
	}
	if !slices.Equal(partial.Tools, []string{"web_search"}) || !slices.Equal(partial.Unavailable, []string{"web_fetch"}) {
		t.Fatalf("partial selection = %+v", partial)
	}
}

// TestF6TSET034SearchBundle: the `search` bundle is composite (no tools of its
// own), includes web, and resolves no unknown or duplicate member.
func TestF6TSET034SearchBundle(t *testing.T) {
	t.Parallel()
	def := webBuiltin(t, "search")
	if len(def.Tools) != 0 || !slices.Equal(def.Includes, []string{"web"}) {
		t.Fatalf("search bundle = %+v", def)
	}
	avail := available("web_fetch", "web_search", "shell")
	sel, err := Resolve(Config{Active: []string{"search"}}, "linux", avail)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := []string{"web_fetch", "web_search"}; !slices.Equal(sel.Tools, want) {
		t.Fatalf("bundle tools = %v, want %v", sel.Tools, want)
	}
	if len(sel.Unavailable) != 0 {
		t.Fatalf("bundle unavailable = %v", sel.Unavailable)
	}
	include := Config{Custom: map[string]Definition{"mine": {Includes: []string{"search"}}}}
	if err := include.Validate(); err != nil {
		t.Fatalf("bundle is not a valid include: %v", err)
	}
	shadow := Config{Custom: map[string]Definition{"search": {Tools: []string{"web_fetch"}}}}
	if err := shadow.Validate(); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("custom bundle shadow = %v, want ErrDuplicate", err)
	}
}
