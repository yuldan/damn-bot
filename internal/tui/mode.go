package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
)

// RenderMode is resolved once at assembly (NO_COLOR, TERM=dumb, --plain, or a
// non-TTY stdout) and drives every style and width/height constraint in the
// TUI. A plain mode never styles text; it truncates without escapes.
type RenderMode struct {
	NoColor bool
}

// newStyles is the single palette entry point: neutral fg/muted/border, one
// accent, and three semantic colors used only for status glyphs. Dim is
// metadata-only; bold is title/active-tab only.
func newStyles(t *Theme, noColor bool) styles {
	if noColor {
		return styles{}
	}
	dim := func(s lipgloss.Style) lipgloss.Style {
		if t.dim {
			return s.Faint(true)
		}
		return s
	}
	header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.accent))
	tabActive := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.fg))
	tabIdle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.muted))
	fg := lipgloss.NewStyle().Foreground(lipgloss.Color(t.fg))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color(t.muted))
	border := dim(lipgloss.NewStyle().Foreground(lipgloss.Color(t.border)))
	return styles{
		header: header, tabActive: tabActive, tabIdle: tabIdle,
		fg: fg, muted: muted, dim: dim(muted), border: border, separator: border,
		footer: muted, hint: dim(muted), status: muted,
		user: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.fg)),
		tool: fg, notice: lipgloss.NewStyle().Foreground(lipgloss.Color(t.warn)),
		success: lipgloss.NewStyle().Foreground(lipgloss.Color(t.success)),
		warn:    lipgloss.NewStyle().Foreground(lipgloss.Color(t.warn)),
		error:   lipgloss.NewStyle().Foreground(lipgloss.Color(t.error)),
		modal:   lipgloss.NewStyle().Foreground(lipgloss.Color(t.fg)).Background(lipgloss.Color(t.modalBg)).Padding(1, 2),
	}
}

func (m *Model) limitWidth(s string) string {
	if m.width <= 0 {
		return s
	}
	if m.noColor {
		return truncatePlain(s, m.width)
	}
	if lipgloss.Width(s) <= m.width {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(m.width-1).Render(s) + "…"
}

func (m *Model) limitHeight(s string) string {
	reserved := 5 + len(m.draft)
	if m.height <= reserved {
		return s
	}
	if m.noColor {
		return tailPlain(s, m.height-reserved)
	}
	return lipgloss.NewStyle().MaxHeight(m.height - reserved).Render(s)
}

// truncatePlain bounds one line without emitting any escape sequence.
func truncatePlain(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width <= 1 {
		return string(r[:max(0, width)])
	}
	return string(r[:width-1]) + "…"
}

// tailPlain keeps the last n lines without styling.
func tailPlain(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// styleInput keeps the prompt inside the same palette (no library default 240).
func (m *Model) styleInput() {
	if m.noColor {
		m.input.SetStyles(textinput.Styles{})
		return
	}
	state := textinput.StyleState{Text: m.styles.fg, Placeholder: m.styles.dim, Prompt: m.styles.muted}
	m.input.SetStyles(textinput.Styles{Focused: state, Blurred: state})
}
