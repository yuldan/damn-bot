package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// RenderMode is resolved once at assembly (NO_COLOR, TERM=dumb, --plain, or a
// non-TTY stdout) and drives every style and width/height constraint in the
// TUI. A plain mode never styles text; it truncates without escapes.
type RenderMode struct {
	NoColor bool
}

func newStyles(t Theme, noColor bool) styles {
	if noColor {
		return styles{}
	}
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

func (m *Model) limitWidth(s string) string {
	if m.width <= 0 {
		return s
	}
	if m.noColor {
		return truncatePlain(s, m.width)
	}
	return lipgloss.NewStyle().MaxWidth(m.width).Render(s)
}

func (m *Model) limitHeight(s string) string {
	reserved := 4 + len(m.draft)
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
	return string(r[:width])
}

// tailPlain keeps the last n lines without styling.
func tailPlain(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
