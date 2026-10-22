package e2e

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestSessionsAdminEndToEnd drives the real binary: a headless turn creates a
// transcript, then rename and export operate on it.
func TestSessionsAdminEndToEnd(t *testing.T) {
	t.Parallel()
	const secret = "sk-abcdefghijklmnopqrstuvwxyz0123456789"
	e := New(t, providertest.Text("reply "+secret+" and <b>markup</b>"))
	e.Run("hello session admin").RequireOK(t)

	ls := e.Exec("sessions", "ls", "--data-dir", e.DataDir)
	ls.RequireOK(t)
	id, _, _ := strings.Cut(strings.TrimSpace(ls.Stdout), "\t")
	if id == "" {
		t.Fatalf("sessions ls printed no id:\n%s", ls.Stdout)
	}

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
	if strings.Contains(exp.Stdout, secret) || !strings.Contains(exp.Stdout, "[REDACTED:") {
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
	run := e.Exec("sessions", "prune", "--older-than", "1ns", "--yes", "--data-dir", e.DataDir)
	run.RequireOK(t)
	if !strings.Contains(run.Stdout, "purged 1") {
		t.Errorf("purge = %q", run.Stdout)
	}
	after := e.Exec("sessions", "ls", "--data-dir", e.DataDir)
	after.RequireOK(t)
	if strings.TrimSpace(after.Stdout) != "" {
		t.Errorf("sessions remain after purge:\n%s", after.Stdout)
	}
}
