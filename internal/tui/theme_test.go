package tui

import (
	"slices"
	"testing"
)

// TestThemeRegistry pins the four SPEC §13.1 themes (Q2) and the picker seam.
func TestThemeRegistry(t *testing.T) {
	t.Parallel()
	want := []string{"tern-dark", "tern-light", "mono", "high-contrast"}
	got := Themes()
	if len(got) != len(want) {
		t.Fatalf("Themes() = %d entries, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Fatalf("Themes()[%d] = %q, want %q", i, got[i].Name, name)
		}
		th, ok := ThemeByName(name)
		if !ok || th.Name != name {
			t.Fatalf("ThemeByName(%q) = %q, %v", name, th.Name, ok)
		}
	}
	if _, ok := ThemeByName("solarized"); ok {
		t.Fatalf("ThemeByName accepted an unknown theme")
	}
	if len(Themes()) != len(got) || Themes()[0].Name != want[0] {
		t.Fatalf("Themes() does not return a deterministic registry")
	}
}

// TestThemePaletteValues pins the measured design palette.
func TestThemePaletteValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                                                string
		theme                                               Theme
		fg, muted, border, accent, success, warn, error, bg string
		dim                                                 bool
	}{
		{"tern-dark", mustTheme(t, "tern-dark"), "252", "244", "238", "39", "108", "179", "167", "236", true},
		{"tern-light", mustTheme(t, "tern-light"), "236", "243", "250", "33", "28", "130", "124", "254", true},
		{"mono", mustTheme(t, "mono"), "", "", "", "", "", "", "", "", false},
		{"high-contrast", mustTheme(t, "high-contrast"), "15", "15", "15", "15", "10", "11", "9", "", false},
	}
	for _, tc := range cases {
		got := []string{tc.theme.fg, tc.theme.muted, tc.theme.border, tc.theme.accent, tc.theme.success, tc.theme.warn, tc.theme.error, tc.theme.modalBg}
		want := []string{tc.fg, tc.muted, tc.border, tc.accent, tc.success, tc.warn, tc.error, tc.bg}
		if !slices.Equal(got, want) || tc.theme.dim != tc.dim {
			t.Fatalf("%s = %+v, want %v dim=%v", tc.name, tc.theme, want, tc.dim)
		}
	}
}

func mustTheme(t *testing.T, name string) Theme {
	t.Helper()
	th, ok := ThemeByName(name)
	if !ok {
		t.Fatalf("ThemeByName(%q) failed", name)
	}
	return th
}

// TestThemeFromConfig: the [ui] theme snapshot selects the palette; an empty
// or unknown name falls back to tern-dark instead of failing the first frame.
func TestThemeFromConfig(t *testing.T) {
	t.Parallel()
	cases := []struct{ theme, want string }{
		{"", "tern-dark"},
		{"tern-dark", "tern-dark"},
		{"tern-light", "tern-light"},
		{"mono", "mono"},
		{"high-contrast", "high-contrast"},
		{"solarized", "tern-dark"},
	}
	for _, tc := range cases {
		m, err := NewApp("sess-theme", testHost(
			newFakeClock(), &fakeRunner{},
			&fakeConfig{cfg: Config{Model: "fake/model", Theme: tc.theme}},
		))
		if err != nil {
			t.Fatalf("NewApp(%q): %v", tc.theme, err)
		}
		if m.theme.Name != tc.want {
			t.Fatalf("theme %q resolved to %q, want %q", tc.theme, m.theme.Name, tc.want)
		}
	}
}
