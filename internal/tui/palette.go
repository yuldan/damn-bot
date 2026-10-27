package tui

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

func (m *Model) openPalette() {
	m.palette, m.paletteQuery, m.paletteSel = true, "", 0
}

func (m *Model) paletteItems() []slashItem {
	q := strings.ToLower(m.paletteQuery)
	var out []slashItem
	for _, it := range slashCatalog() {
		if q == "" || strings.Contains(strings.ToLower(it.name+" "+it.desc), q) {
			out = append(out, it)
		}
	}
	return out
}

func (m *Model) paletteBody() string {
	items := m.paletteItems()
	var b strings.Builder
	fmt.Fprintf(&b, "PALETTE /%s\n", m.paletteQuery)
	if len(items) == 0 {
		b.WriteString("no matching command")
		return b.String()
	}
	m.paletteSel = max(0, min(len(items)-1, m.paletteSel))
	for i, it := range items {
		mark := " "
		if i == m.paletteSel {
			mark = "▸"
		}
		fmt.Fprintf(&b, "%s %-10s %s\n", mark, it.name, it.desc)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func (m *Model) handlePaletteKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case msg.Code == tea.KeyEsc || msg.String() == "ctrl+p":
		m.palette = false
	case msg.Code == tea.KeyUp:
		m.paletteSel = max(0, m.paletteSel-1)
	case msg.Code == tea.KeyDown:
		m.paletteSel++
	case msg.Code == tea.KeyBackspace:
		if m.paletteQuery != "" {
			r := []rune(m.paletteQuery)
			m.paletteQuery = string(r[:len(r)-1])
		}
		m.paletteSel = 0
	case msg.Code == tea.KeyEnter:
		items := m.paletteItems()
		if len(items) == 0 {
			return nil
		}
		it := items[max(0, min(len(items)-1, m.paletteSel))]
		m.palette = false
		cmd, _ := parseSlash(it.name)
		return m.runSlash(cmd)
	default:
		if msg.Mod == 0 && msg.Text != "" && printable(msg.Text) {
			m.paletteQuery += msg.Text
			m.paletteSel = 0
		}
	}
	return nil
}

func printable(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}
