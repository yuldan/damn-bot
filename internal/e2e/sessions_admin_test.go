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
}
