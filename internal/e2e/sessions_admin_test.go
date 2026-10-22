package e2e

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

const e2eSecret = "sk-abcdefghijklmnopqrstuvwxyz0123456789"

// sessionAdminEnv runs one turn and returns the env plus its session id.
func sessionAdminEnv(t *testing.T) (*Env, string) {
	t.Helper()
	e := New(t, providertest.Text("reply "+e2eSecret+" and <b>markup</b>"))
	e.Run("hello session admin").RequireOK(t)
	ls := e.Exec("sessions", "ls", "--data-dir", e.DataDir)
	ls.RequireOK(t)
	id, _, _ := strings.Cut(strings.TrimSpace(ls.Stdout), "\t")
	if id == "" {
		t.Fatalf("sessions ls printed no id:\n%s", ls.Stdout)
	}
	return e, id
}

// TestSessionsAdminExport: rename/export/stats/pin through the real binary.
func TestSessionsAdminExport(t *testing.T) {
	t.Parallel()
	e, id := sessionAdminEnv(t)
	e.Exec("sessions", "rename", id, "E2E Title", "--data-dir", e.DataDir).RequireOK(t)
	ren := e.Exec("sessions", "rename", id, "--data-dir", e.DataDir)
	ren.RequireOK(t)
	if !strings.Contains(ren.Stdout, "title: E2E Title") {
		t.Errorf("rename read = %q", ren.Stdout)
	}
	exp := e.Exec("sessions", "export", id, "--data-dir", e.DataDir)
	exp.RequireOK(t)
	if !strings.HasPrefix(exp.Stdout, "# E2E Title\n") || !strings.Contains(exp.Stdout, "hello session admin") {
		t.Errorf("markdown export:\n%s", exp.Stdout)
	}
	if strings.Contains(exp.Stdout, e2eSecret) || !strings.Contains(exp.Stdout, "[REDACTED:") {
		t.Errorf("export did not redact the secret:\n%s", exp.Stdout)
	}
	html := e.Exec("sessions", "export", id, "--format", "html", "--data-dir", e.DataDir)
	html.RequireOK(t)
	if strings.Contains(html.Stdout, "<b>markup</b>") || !strings.Contains(html.Stdout, "&lt;b&gt;markup&lt;/b&gt;") {
		t.Errorf("html export did not escape content:\n%s", html.Stdout)
	}
	stats := e.Exec("sessions", "stats", "--data-dir", e.DataDir)
	stats.RequireOK(t)
	if !strings.Contains(stats.Stdout, "sessions: 1") || !strings.Contains(stats.Stdout, "events: ") {
		t.Errorf("stats:\n%s", stats.Stdout)
	}
	e.Exec("sessions", "pin", id, "--data-dir", e.DataDir).RequireOK(t)
	pinned := e.Exec("sessions", "pinned", "--data-dir", e.DataDir)
	pinned.RequireOK(t)
	if !strings.Contains(pinned.Stdout, id) {
		t.Errorf("pinned list = %q", pinned.Stdout)
	}
}

// TestSessionsAdminPrune: archive, dry-run and purge through the real binary.
func TestSessionsAdminPrune(t *testing.T) {
	t.Parallel()
	e, id := sessionAdminEnv(t)
	e.Exec("sessions", "archive", id, "--data-dir", e.DataDir).RequireOK(t)
	arch := e.Exec("sessions", "stats", "--data-dir", e.DataDir)
	arch.RequireOK(t)
	if !strings.Contains(arch.Stdout, "archived: 1") {
		t.Errorf("stats after archive:\n%s", arch.Stdout)
	}
	dry := e.Exec("sessions", "prune", "--older-than", "1ns", "--dry-run", "--data-dir", e.DataDir)
	dry.RequireOK(t)
	if !strings.Contains(dry.Stdout, "would purge 1") {
		t.Errorf("dry run = %q", dry.Stdout)
	}
	purge := e.Exec("sessions", "prune", "--older-than", "1ns", "--yes", "--data-dir", e.DataDir)
	purge.RequireOK(t)
	if !strings.Contains(purge.Stdout, "purged 1") {
		t.Errorf("purge = %q", purge.Stdout)
	}
	after := e.Exec("sessions", "ls", "--data-dir", e.DataDir)
	after.RequireOK(t)
	if strings.TrimSpace(after.Stdout) != "" {
		t.Errorf("sessions remain after purge:\n%s", after.Stdout)
	}
}
