package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// Layout constants: one-cell gutter, a fixed 28-column side panel (1 cell
// inner padding, 8-column label, right-aligned value), and a thin separator.
const (
	gutterWidth = 1
	panelWidth  = 28
	labelWidth  = 8
)

func (m *Model) separator() string {
	width := m.width
	if width <= 0 {
		width = 40
	}
	ch := "─"
	if m.noColor {
		ch = "-"
	}
	return m.styles.separator.Render(strings.Repeat(ch, width))
}

// chatLayout joins the transcript and the fixed context panel with a one-cell
// gutter. Narrow terminals keep the full-width transcript.
func (m *Model) chatLayout(left string) string {
	if m.width < 60 {
		return left
	}
	leftWidth := m.width - panelWidth - gutterWidth
	leftLines := strings.Split(left, "\n")
	rightLines := strings.Split(m.sidePanel(), "\n")
	n := max(len(leftLines), len(rightLines))
	gutter := "│"
	if m.noColor {
		gutter = "|"
	}
	rows := make([]string, 0, n)
	for i := range n {
		l, r := "", ""
		if i < len(leftLines) {
			l = leftLines[i]
		}
		if i < len(rightLines) {
			r = rightLines[i]
		}
		l = m.fit(l, leftWidth)
		r = m.fit(r, panelWidth)
		rows = append(rows, l+m.styles.border.Render(gutter)+r)
	}
	return strings.Join(rows, "\n")
}

// fit truncates or right-pads one rendered line to exactly width cells.
func (m *Model) fit(s string, width int) string {
	if m.noColor {
		s = truncatePlain(s, width)
	} else if lipgloss.Width(s) > width {
		s = lipgloss.NewStyle().MaxWidth(width-1).Render(s) + "…"
	}
	if pad := width - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func panelLine(label, value string, labelStyle, valueStyle *lipgloss.Style) string {
	inner := panelWidth - 2
	if len(label) > labelWidth {
		label = label[:labelWidth]
	}
	gap := max(1, inner-labelWidth-len(value))
	line := " " + labelStyle.Render(label+strings.Repeat(" ", labelWidth-len(label))) +
		strings.Repeat(" ", gap) + valueStyle.Render(value)
	return line + strings.Repeat(" ", max(0, panelWidth-lipgloss.Width(line)))
}

func (m *Model) sidePanel() string {
	window, cache, fixed := "—", "—", "—"
	if m.contextInfo.Window > 0 {
		window = fmt.Sprintf("%d", m.contextInfo.Window)
	}
	if m.usage.Input > 0 {
		cache = fmt.Sprintf("%.0f%%", 100*float64(m.usage.CachedInput)/float64(m.usage.Input))
	}
	turn, turnOK := m.turnCost()
	session, sessionOK := m.sessionCost()
	if m.busy {
		fixed = "…"
	}
	lines := []string{
		panelLine("CONTEXT", "", &m.styles.tabActive, &m.styles.fg),
		panelLine("fixed", fixed, &m.styles.dim, &m.styles.fg),
		panelLine("window", window, &m.styles.dim, &m.styles.fg),
		panelLine("cache", cache, &m.styles.dim, &m.styles.fg),
		panelLine("TURN", "", &m.styles.tabActive, &m.styles.fg),
		panelLine("iter", fmt.Sprintf("%d / 40", m.usage.Iterations), &m.styles.dim, &m.styles.fg),
		panelLine("cost", fmt.Sprintf("%s / %s", usd(turn, turnOK), usd(session, sessionOK)), &m.styles.dim, &m.styles.fg),
		panelLine("FALLBACK", boolWord(m.fallback, "on", "—"), &m.styles.dim, &m.styles.fg),
	}
	return strings.Join(lines, "\n")
}

func boolWord(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

func (m *Model) statusStyle(s ToolStatus) lipgloss.Style {
	switch s {
	case ToolOK:
		return m.styles.success
	case ToolFailed:
		return m.styles.error
	case ToolPending, ToolRunning:
		return m.styles.warn
	default:
		return m.styles.muted
	}
}

// toolBlock is the hierarchy-preserving tool row: name fg, metadata dim, the
// status glyph alone carries semantic color, arguments/results are muted.
func (m *Model) toolHeader(b *Block) string {
	name := b.ToolName
	if name == "" {
		name = "?"
	}
	var s strings.Builder
	s.WriteString(m.styles.tool.Render("tool " + name))
	if b.Tag != "" {
		s.WriteString(" " + m.styles.dim.Render("#"+b.Tag))
	}
	s.WriteString(" " + m.statusStyle(b.Status).Render(statusGlyph(b.Status)))
	if b.Duration > 0 {
		s.WriteString(" " + m.styles.dim.Render(formatDuration(b.Duration)))
	}
	if b.Size > 0 {
		s.WriteString(" " + m.styles.muted.Render(formatSize(b.Size)))
	}
	if b.ExitCode != nil && b.Status == ToolFailed {
		s.WriteString(" " + m.styles.muted.Render(fmt.Sprintf("exit %d", *b.ExitCode)))
	}
	if b.ActionID != 0 {
		s.WriteString(" " + m.styles.dim.Render(fmt.Sprintf("↶ %d", b.ActionID)))
	}
	return s.String()
}

func (m *Model) toolBlock(b *Block) string {
	var s strings.Builder
	s.WriteString(m.toolHeader(b))
	if brief := briefArgs(b.Args); brief != "" {
		s.WriteString("\n  " + m.styles.muted.Render(brief))
	}
	if b.Spill != "" {
		line := "spill: " + b.Spill
		if b.SpillSize > 0 {
			line += " (" + formatSize(b.SpillSize) + " chars)"
		}
		s.WriteString("\n  " + m.styles.muted.Render(line))
	}
	if preview := resultPreview(b.Text); preview != "" {
		s.WriteString("\n  " + m.styles.muted.Render(preview))
	}
	return s.String()
}

func (m *Model) modalBody(body string) string {
	if m.noColor {
		return body
	}
	modal := m.styles.modal
	if m.width > 4 {
		modal = modal.Width(min(60, m.width-4))
	}
	return modal.Render(body)
}
