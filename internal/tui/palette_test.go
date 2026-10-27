package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func textKey(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func TestPaletteFiltersAndRunsSlash(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestApp(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if !m.palette || !strings.Contains(m.View().Content, "PALETTE /") {
		t.Fatalf("ctrl+p did not open the palette")
	}
	for _, r := range "the" {
		m.Update(textKey(r))
	}
	view := m.View().Content
	if !strings.Contains(view, "/theme") || strings.Contains(view, "/new") {
		t.Fatalf("palette filter = %q:\n%s", m.paletteQuery, view)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.palette {
		t.Fatalf("enter did not close the palette")
	}
	if !strings.Contains(lastBlockText(m), "theme tern-dark; valid:") {
		t.Fatalf("palette did not run /theme: %q", lastBlockText(m))
	}
	if m.theme.Name != "tern-dark" {
		t.Fatalf("palette changed the theme unexpectedly")
	}
}

func TestPaletteEscAndUnknown(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestApp(t)
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.palette {
		t.Fatalf("esc did not close the palette")
	}
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	for _, r := range "zzz" {
		m.Update(textKey(r))
	}
	if !strings.Contains(m.View().Content, "no matching command") {
		t.Fatalf("empty palette filter not reported")
	}
}

// TestPaletteUsesSlashPath: selecting /reload runs the same handler (and the
// same refusal) as typing it.
func TestPaletteUsesSlashPath(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestApp(t)
	rt, ok := m.host.Runtime.(*fakeRuntime)
	if !ok {
		t.Fatalf("runtime = %T", m.host.Runtime)
	}
	rt.reloadErr = errors.New("ui.theme: invalid")
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	for _, r := range "reload" {
		m.Update(textKey(r))
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		m.Update(cmd())
	}
	if !strings.Contains(lastBlockText(m), "reload refused") {
		t.Fatalf("palette bypassed the slash handler: %q", lastBlockText(m))
	}
}
