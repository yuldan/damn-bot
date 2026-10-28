package e2e

import (
	"os"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestE2ENoticePolicyConfig (GAP-OPS-09): [ui] notices defaults to true, can be
// turned off permanently, and a bad value fails closed with the key named.
func TestE2ENoticePolicyConfig(t *testing.T) {
	t.Parallel()
	e := New(t)
	res := e.Exec("config", "get", "ui.notices")
	res.RequireOK(t)
	if strings.TrimSpace(res.Stdout) != "true" {
		t.Fatalf("default ui.notices = %q, want true", res.Stdout)
	}

	writeProfileConfig(t, e, "[ui]\nnotices = false\n")
	res = e.Exec("config", "get", "ui.notices")
	res.RequireOK(t)
	if strings.TrimSpace(res.Stdout) != "false" {
		t.Fatalf("configured ui.notices = %q, want false", res.Stdout)
	}

	writeProfileConfig(t, e, "[ui]\nnotices = \"off\"\n")
	res = e.Exec("config", "show")
	if res.Code == 0 || !strings.Contains(res.Stderr, "ui.notices") {
		t.Fatalf("invalid notices: exit %d\nstderr: %s", res.Code, res.Stderr)
	}
}

// TestE2EJSONModeHasNoNotice: the TUI tip never leaks into a headless JSON
// surface (GAP-OPS-09: non-critical notices are a frontend concern).
func TestE2EJSONModeHasNoNotice(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("ok"))
	if err := os.WriteFile(e.cfg, []byte("[ui]\nnotices = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.Run("hello")
	res.RequireOK(t)
	if strings.Contains(res.Stdout+res.Stderr, "tip:") || strings.Contains(res.Stdout+res.Stderr, "ctrl+p") {
		t.Fatalf("JSON run leaked a notice:\nstdout: %s\nstderr: %s", res.Stdout, res.Stderr)
	}
}
