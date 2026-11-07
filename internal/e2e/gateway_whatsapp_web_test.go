package e2e

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestF7E2EWhatsAppWebCLI is the GAP-GW-01 end-to-end control on the real
// binary: `gateway whatsapp-web enable` refuses without the acknowledgement
// and writes nothing; with the flag it enables the bridge and never prints a
// token or QR. The bridge itself is not spawned here (0 network, 0 child).
func TestF7E2EWhatsAppWebCLI(t *testing.T) {
	t.Parallel()
	e := New(t)
	file := setupFile(t, e)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("[provider]\nmodel = \"openai/gpt-test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := setupHash(t, file)

	denied := e.Exec("gateway", "whatsapp-web", "enable")
	if denied.Code != 2 || !strings.Contains(denied.Stderr, "accept-ban-risk") {
		t.Fatalf("denied = %d (%s)", denied.Code, denied.Stderr)
	}
	if setupHash(t, file) != before {
		t.Fatal("denied enable wrote the config")
	}

	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	enabled := e.Exec("gateway", "whatsapp-web", "enable", "--accept-ban-risk", "--data-dir", dataDir)
	enabled.RequireOK(t)
	if out := strings.ToLower(enabled.Stdout + enabled.Stderr); strings.Contains(out, "qr") || strings.Contains(out, "token") {
		t.Fatalf("enable printed a credential or QR: %s%s", enabled.Stdout, enabled.Stderr)
	}
	cfg, err := fs.ReadFile(os.DirFS(filepath.Dir(file)), filepath.Base(file))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "[modules.gateway.whatsapp_web]") || !strings.Contains(string(cfg), "enabled = true") {
		t.Fatalf("config after enable:\n%s", cfg)
	}

	status := e.Exec("gateway", "whatsapp-web", "status")
	status.RequireOK(t)
	if !strings.Contains(status.Stdout, "enabled=true") || strings.Contains(status.Stdout, "Bearer") {
		t.Fatalf("status = %s", status.Stdout)
	}
}
