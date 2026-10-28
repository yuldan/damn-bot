package tui

import "strings"

// Theme is one SPEC §13.1 palette (Q2): neutral text/border, one accent, and
// semantic status colors used only on glyphs. Background exists solely for the
// approval modal.
type Theme struct {
	Name                          string
	fg, muted, border, accent     string
	success, warn, error, modalBg string
	dim                           bool
}

const defaultThemeName = "tern-dark"

// Themes returns the four SPEC §13.1 themes in registry order.
func Themes() []Theme {
	return []Theme{
		{Name: "tern-dark", fg: "252", muted: "244", border: "238", accent: "39", success: "108", warn: "179", error: "167", modalBg: "236", dim: true},
		{Name: "tern-light", fg: "236", muted: "243", border: "250", accent: "33", success: "28", warn: "130", error: "124", modalBg: "254", dim: true},
		{Name: "mono"},
		{Name: "high-contrast", fg: "15", muted: "15", border: "15", accent: "15", success: "10", warn: "11", error: "9"},
	}
}

// ThemeByName resolves one theme. ok is false for an unknown name; the caller
// decides whether to fall back (NewApp uses defaultThemeName).
func ThemeByName(name string) (Theme, bool) {
	themes := Themes()
	for i := range themes {
		if themes[i].Name == name {
			return themes[i], true
		}
	}
	return Theme{}, false
}

// themeNames lists the registry for /theme refusals and help.
func themeNames() string {
	themes := Themes()
	names := make([]string, 0, len(themes))
	for i := range themes {
		names = append(names, themes[i].Name)
	}
	return strings.Join(names, ", ")
}
