package tui

import "testing"

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

// TestThemeMonoIsColorless keeps the accessibility theme free of color tokens;
// high-contrast and tern-dark must actually differ.
func TestThemeMonoIsColorless(t *testing.T) {
	t.Parallel()
	mono, _ := ThemeByName("mono")
	if mono.primary != "" || mono.muted != "" || mono.alert != "" {
		t.Fatalf("mono carries color tokens: %+v", mono)
	}
	dark, _ := ThemeByName("tern-dark")
	if dark.primary == "" || dark.alert == "" {
		t.Fatalf("tern-dark has no color tokens: %+v", dark)
	}
	hc, _ := ThemeByName("high-contrast")
	if hc.primary == dark.primary && hc.muted == dark.muted && hc.alert == dark.alert {
		t.Fatalf("high-contrast is identical to tern-dark")
	}
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
			fakeConfig{cfg: Config{Model: "fake/model", Theme: tc.theme}},
		))
		if err != nil {
			t.Fatalf("NewApp(%q): %v", tc.theme, err)
		}
		if m.theme.Name != tc.want {
			t.Fatalf("theme %q resolved to %q, want %q", tc.theme, m.theme.Name, tc.want)
		}
	}
}
