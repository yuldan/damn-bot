package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Theme is one SPEC §13.1 palette (Q2). The color tokens are unexported: the
// registry is the only constructor, so a caller can never build a half theme.
// ThemeByName and Themes are the picker seam for the `theme` setting; the
// renderer consumes the resolved Theme through styles.
type Theme struct {
	Name    string
	primary string // titles, user prompt, cursor accents
	muted   string // tool output, status bar, hints
	alert   string // notices, fallback badge
}

// defaultThemeName is the SPEC §12 [ui] default and the fallback for a config
// snapshot that carries no theme (the config validator closes the set
// upstream, so an unknown name can only come from a custom seam).
const defaultThemeName = "tern-dark"

// Themes returns the four SPEC §13.1 themes in registry order. The slice is
// fresh on every call; callers may not mutate shared state.
func Themes() []Theme {
	return []Theme{
		{Name: "tern-dark", primary: "12", muted: "8", alert: "9"},
		{Name: "tern-light", primary: "4", muted: "8", alert: "1"},
		{Name: "mono"},
		{Name: "high-contrast", primary: "15", muted: "15", alert: "9"},
	}
}

// ThemeByName resolves one theme. ok is false for an unknown name; the caller
// decides whether to fall back (NewApp uses defaultThemeName).
func ThemeByName(name string) (Theme, bool) {
	for _, t := range Themes() {
		if t.Name == name {
			return t, true
		}
	}
	return Theme{}, false
}

// themeNames lists the registry for /theme refusals and help.
func themeNames() string {
	names := make([]string, 0, len(Themes()))
	for _, t := range Themes() {
		names = append(names, t.Name)
	}
	return strings.Join(names, ", ")
}

// newStyles maps a theme to the T0 call-site styles. mono (no tokens) leaves
// every style colorless while keeping the structural bold on headers and the
// user prompt.
func newStyles(t Theme) styles {
	header := lipgloss.NewStyle().Bold(true)
	user := lipgloss.NewStyle().Bold(true)
	footer := lipgloss.NewStyle()
	hint := lipgloss.NewStyle()
	status := lipgloss.NewStyle()
	tool := lipgloss.NewStyle()
	notice := lipgloss.NewStyle()
	if t.primary != "" {
		header = header.Foreground(lipgloss.Color(t.primary))
		user = user.Foreground(lipgloss.Color(t.primary))
		footer = footer.Faint(true)
		hint = hint.Faint(true)
		status = status.Faint(true).Foreground(lipgloss.Color(t.muted))
		tool = tool.Faint(true).Foreground(lipgloss.Color(t.muted))
		notice = notice.Foreground(lipgloss.Color(t.alert))
	}
	return styles{header: header, footer: footer, hint: hint, status: status, user: user, tool: tool, notice: notice}
}
