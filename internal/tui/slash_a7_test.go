package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func a7App(t *testing.T) (*Model, *fakeRuntime, *fakeFiles, *fakeConfig) {
	t.Helper()
	m, _, _ := newTestApp(t)
	fcfg := &fakeConfig{cfg: Config{Model: "fake/model"}}
	m.host.Config = fcfg
	rt, ok := m.host.Runtime.(*fakeRuntime)
	if !ok {
		t.Fatalf("runtime = %T", m.host.Runtime)
	}
	files, ok := m.host.Files.(*fakeFiles)
	if !ok {
		t.Fatalf("files = %T", m.host.Files)
	}
	return m, rt, files, fcfg
}

// TestReloadAppliesLiveSnapshot: a successful reload swaps theme/budget in the
// running model and reports the new hash plus what needs a restart.
func TestReloadAppliesLiveSnapshot(t *testing.T) {
	t.Parallel()
	m, rt, _, fcfg := a7App(t)
	rt.hash = "oldhash"
	rt.reload = ReloadResult{Hash: "0123456789abcdef", Applied: []string{"theme mono"}, Restart: []string{"loop"}}
	rt.onReload = func() {
		fcfg.cfg.Theme = "mono"
		fcfg.cfg.Budget = BudgetInfo{TurnUSD: 9}
	}
	rt.ConfigHash()
	runSlashText(t, m, "/reload")
	if m.theme.Name != "mono" {
		t.Fatalf("theme = %q after reload, want mono", m.theme.Name)
	}
	notice := lastNotice(m)
	for _, want := range []string{"reloaded 0123456789ab", "applied: theme mono", "restart: loop"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("reload notice missing %q: %q", want, notice)
		}
	}
}

// TestReloadFailClosed: an invalid config keeps the old snapshot and theme.
func TestReloadFailClosed(t *testing.T) {
	t.Parallel()
	m, rt, _, _ := a7App(t)
	rt.reloadErr = errors.New("ui.theme: must be tern-dark, tern-light, mono or high-contrast")
	runSlashText(t, m, "/reload")
	if m.theme.Name != "tern-dark" {
		t.Fatalf("failed reload changed theme to %q", m.theme.Name)
	}
	if !strings.Contains(lastNotice(m), "old snapshot kept") {
		t.Fatalf("fail-closed notice missing: %q", lastNotice(m))
	}
}

func TestReloadRefusedDuringTurn(t *testing.T) {
	t.Parallel()
	m, rt, _, _ := a7App(t)
	m.busy = true
	runSlashText(t, m, "/reload")
	if rt.reloadCalls != 0 {
		t.Fatalf("reload ran during a turn")
	}
	if !strings.Contains(lastNotice(m), "/reload refused") {
		t.Fatalf("busy reload not refused: %q", lastNotice(m))
	}
}

// TestExportConfirmWrites: /export never writes before y, and the confirmed
// write carries the transcript content to the explicit path.
func TestExportConfirmWrites(t *testing.T) {
	t.Parallel()
	m, _, files, _ := a7App(t)
	m.appendBlock(&Block{Kind: BlockUser, Text: "halo export"})
	runSlashText(t, m, "/export out.txt")
	if m.confirm == nil {
		t.Fatalf("/export did not ask for confirmation")
	}
	if files.writes != 0 {
		t.Fatalf("write happened before confirmation")
	}
	if view := m.View().Content; !strings.Contains(view, "WRITE export · out.txt") {
		t.Fatalf("confirmation missing path:\n%s", view)
	}
	m.Update(navKey('n', 0))
	if files.writes != 0 || m.confirm != nil {
		t.Fatalf("cancel wrote the file")
	}

	runSlashText(t, m, "/export out.txt")
	m.Update(m.handleKey(navKey('y', 0))())
	if files.writes != 1 {
		t.Fatalf("writes = %d, want 1", files.writes)
	}
	if !strings.Contains(files.text, "halo export") || !strings.Contains(files.text, m.sessionID) {
		t.Fatalf("export body = %q", files.text)
	}
	if !strings.Contains(lastNotice(m), "wrote /abs/out.txt") {
		t.Fatalf("write notice = %q", lastNotice(m))
	}
}

// TestInitWritesTemplate: /init defaults to AGENTS.md, asks first, and writes
// the template only on y.
func TestInitWritesTemplate(t *testing.T) {
	t.Parallel()
	m, _, files, _ := a7App(t)
	runSlashText(t, m, "/init")
	if m.confirm == nil || m.confirm.path != "AGENTS.md" {
		t.Fatalf("/init confirm = %+v", m.confirm)
	}
	if files.writes != 0 {
		t.Fatalf("/init wrote before confirmation")
	}
	m.Update(m.handleKey(navKey('y', 0))())
	if !strings.Contains(files.text, "# AGENTS.md") {
		t.Fatalf("init body = %q", files.text)
	}
}

func TestExportWriteError(t *testing.T) {
	t.Parallel()
	m, _, files, _ := a7App(t)
	files.writeErr = errors.New("permission denied")
	runSlashText(t, m, "/export out.txt")
	m.Update(m.handleKey(navKey('y', 0))())
	if !strings.Contains(lastNotice(m), "write refused: permission denied") {
		t.Fatalf("write error hidden: %q", lastNotice(m))
	}
}

// TestDiffCompactReview dispatch real host results into the transcript.
func TestDiffCompactReview(t *testing.T) {
	t.Parallel()
	m, rt, _, _ := a7App(t)
	rt.diff = "a1 file_write /w/x: would restore\n-old\n+new"
	runSlashText(t, m, "/diff")
	if !strings.Contains(lastNotice(m), "a1 file_write /w/x") {
		t.Fatalf("diff notice = %q", lastNotice(m))
	}

	rt.compact = "compaction summarized (estimate 100/200)"
	runSlashText(t, m, "/compact")
	if !strings.Contains(lastNotice(m), "compaction summarized") {
		t.Fatalf("compact notice = %q", lastNotice(m))
	}
	m.busy = true
	runSlashText(t, m, "/compact")
	if !strings.Contains(lastNotice(m), "/compact refused") {
		t.Fatalf("busy compact not refused: %q", lastNotice(m))
	}
	m.busy = false

	rt.review = "7\tuser\tfact\tremember this"
	runSlashText(t, m, "/review")
	if !strings.Contains(lastNotice(m), "remember this") {
		t.Fatalf("review notice = %q", lastNotice(m))
	}
}

func TestBtwRefused(t *testing.T) {
	t.Parallel()
	m, _, _, _ := a7App(t)
	runSlashText(t, m, "/btw pertanyaan sampingan")
	if !strings.Contains(lastNotice(m), "/btw refused") {
		t.Fatalf("/btw = %q", lastNotice(m))
	}
}

// TestConfirmKeysAreExclusive: while a write confirmation is open, the prompt
// does not receive the key and the approval modal cannot open a second path.
func TestConfirmModalBlocksInput(t *testing.T) {
	t.Parallel()
	m, _, _, _ := a7App(t)
	runSlashText(t, m, "/export out.txt")
	m.Update(navKey('x', 0))
	if m.input.Value() != "" {
		t.Fatalf("confirm modal leaked %q into the prompt", m.input.Value())
	}
	if m.confirm == nil {
		t.Fatalf("confirm modal closed on an unrelated key")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.confirm != nil || !strings.Contains(lastNotice(m), "write cancelled") {
		t.Fatalf("esc did not cancel the write")
	}
}
