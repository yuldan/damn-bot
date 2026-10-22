package toolset

import (
	"errors"
	"slices"
	"testing"
)

func available(names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

// TestValidateFailsClosed: a name the binary does not know is a config error,
// not a silent drop.
func TestValidateFailsClosed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  Config
		want error
	}{
		{"unknown active", Config{Active: []string{"nope"}}, ErrUnknownToolset},
		{"unknown disabled", Config{Disabled: []string{"nope"}}, ErrUnknownToolset},
		{"unknown custom tool", Config{Custom: map[string]Definition{
			"mine": {Tools: []string{"read_file", "typo_tool"}},
		}}, ErrUnknownTool},
		{"custom shadows builtin", Config{Custom: map[string]Definition{
			"coding": {Tools: []string{"read_file"}},
		}}, ErrDuplicate},
		{"empty custom", Config{Custom: map[string]Definition{"mine": {}}}, ErrInvalid},
		{"unknown include", Config{Custom: map[string]Definition{
			"mine": {Includes: []string{"nope"}},
		}}, ErrUnknownToolset},
		{"cycle", Config{Custom: map[string]Definition{
			"a": {Includes: []string{"b"}},
			"b": {Includes: []string{"a"}},
		}}, ErrCycle},
		{"platform", Config{Platform: map[string]Override{"macos": {Active: []string{"coding"}}}}, ErrInvalid},
		{"platform unknown name", Config{Platform: map[string]Override{"linux": {Active: []string{"nope"}}}}, ErrUnknownToolset},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); !errors.Is(err, tc.want) {
				t.Fatalf("Validate() = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestValidateAcceptsBuiltins(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Active:   []string{"coding", "*"},
		Disabled: []string{"terminal"},
		Custom: map[string]Definition{
			"review": {Description: "review", Tools: []string{"read_file", "grep"}, Includes: []string{"web"}},
		},
		Platform: map[string]Override{"linux": {Active: []string{"safe"}}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestResolveAvailability: declared-but-unregistered members are returned as
// unavailable, not dropped; only registered tools are exposed.
func TestResolveAvailability(t *testing.T) {
	t.Parallel()
	sel, err := Resolve(Config{Active: []string{"coding"}}, "linux",
		available("read_file", "write_file", "grep", "shell"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := []string{"grep", "read_file", "shell", "write_file"}; !slices.Equal(sel.Tools, want) {
		t.Fatalf("Tools = %v, want %v", sel.Tools, want)
	}
	if slices.Contains(sel.Tools, "memory") {
		t.Fatal("unavailable memory tool exposed")
	}
	if !slices.Contains(sel.Unavailable, "memory") || !slices.Contains(sel.Unavailable, "skills_list") {
		t.Fatalf("Unavailable = %v, want memory and skills_list", sel.Unavailable)
	}
	if !sel.Allowed["shell"] || sel.Allowed["memory"] {
		t.Fatalf("Allowed = %v", sel.Allowed)
	}
}

// TestResolveEmptyActiveIsLegacy: no configured toolset keeps every
// registered tool exposed and Allowed nil (no filtering).
func TestResolveEmptyActiveIsLegacy(t *testing.T) {
	t.Parallel()
	sel, err := Resolve(Config{}, "linux", available("shell", "read_file"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if sel.Allowed != nil || !slices.Equal(sel.Tools, []string{"read_file", "shell"}) {
		t.Fatalf("legacy selection = %+v", sel)
	}
}

// TestResolveDisabledAndCustom: disabled subtracts members; a custom toolset
// composes its own tools and includes.
func TestResolveDisabledAndCustom(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Active:   []string{"mine"},
		Disabled: []string{"web"},
		Custom: map[string]Definition{
			"mine": {Tools: []string{"read_file"}, Includes: []string{"debugging"}},
		},
	}
	sel, err := Resolve(cfg, "linux", available("shell", "read_file", "grep", "web_search"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !slices.Equal(sel.Tools, []string{"grep", "read_file", "shell"}) {
		t.Fatalf("Tools = %v", sel.Tools)
	}
}

// TestResolvePlatformOverride: the platform section replaces both lists.
func TestResolvePlatformOverride(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Active:   []string{"coding"},
		Platform: map[string]Override{"windows": {Active: []string{"safe"}}},
	}
	sel, err := Resolve(cfg, "windows", available("shell", "read_file", "web_search"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !slices.Equal(sel.Tools, []string{"web_search"}) || !slices.Equal(sel.Active, []string{"safe"}) {
		t.Fatalf("windows selection = %+v", sel)
	}
	sel, err = Resolve(cfg, "linux", available("shell", "read_file", "web_search"))
	if err != nil {
		t.Fatalf("Resolve linux: %v", err)
	}
	if !slices.Contains(sel.Tools, "shell") {
		t.Fatalf("linux coding lost shell: %v", sel.Tools)
	}
}

// TestResolveAll: * expands to every toolset (built-in and custom).
func TestResolveAll(t *testing.T) {
	t.Parallel()
	cfg := Config{Active: []string{"*"}, Custom: map[string]Definition{
		"mine": {Tools: []string{"read_file"}},
	}}
	sel, err := Resolve(cfg, "linux", available("read_file", "shell"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !slices.Equal(sel.Tools, []string{"read_file", "shell"}) {
		t.Fatalf("Tools = %v", sel.Tools)
	}
}

// TestContextEngineIsDataOnly: the composition slot carries no tools.
func TestContextEngineIsDataOnly(t *testing.T) {
	t.Parallel()
	sel, err := Resolve(Config{Active: []string{"context_engine"}}, "linux", available("read_file"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(sel.Tools) != 0 || len(sel.Unavailable) != 0 {
		t.Fatalf("context_engine exposed %+v", sel)
	}
}
