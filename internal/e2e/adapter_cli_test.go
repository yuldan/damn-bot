package e2e

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestF7E2EAdapterCLI drives the three adapter setup surfaces on the real
// binary (CLI-015/016/017): the Cloud API is the only offered mode, the Web
// bridge needs `--accept-ban-risk`, the Cloud setup writes secret names into
// config and the config loader reads them back, and the Slack manifest is
// deterministic and credential-free.
func TestF7E2EAdapterCLI(t *testing.T) {
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

	menu := e.Exec("whatsapp", "setup")
	menu.RequireOK(t)
	if strings.Contains(menu.Stdout, "whatsapp_web") {
		t.Fatalf("web offered before the ban-risk ack: %s", menu.Stdout)
	}
	denied := e.Exec("whatsapp", "setup", "--mode", "whatsapp_web")
	if denied.Code != 2 || !strings.Contains(denied.Stderr, "accept-ban-risk") {
		t.Fatalf("web without ack = %d (%s)", denied.Code, denied.Stderr)
	}
	if setupHash(t, file) != before {
		t.Fatal("denied setup wrote the config")
	}

	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	enabled := e.Exec("whatsapp", "setup", "--mode", "whatsapp_web", "--accept-ban-risk", "--data-dir", dataDir)
	enabled.RequireOK(t)
	cfg, err := fs.ReadFile(os.DirFS(filepath.Dir(file)), filepath.Base(file))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "[modules.gateway.whatsapp_web]") || !strings.Contains(string(cfg), "enabled = true") {
		t.Fatalf("config after web setup:\n%s", cfg)
	}

	cloud := e.Exec("whatsapp-cloud", "setup", "--phone-number-id", "123456789012345")
	cloud.RequireOK(t)
	if !strings.Contains(cloud.Stdout, "hub.verify_token") || !strings.Contains(cloud.Stdout, "X-Hub-Signature-256") {
		t.Fatalf("webhook verification missing: %s", cloud.Stdout)
	}
	checkCloudConfig(t, file, e)

	first := e.Exec("slack", "manifest")
	first.RequireOK(t)
	second := e.Exec("slack", "manifest")
	second.RequireOK(t)
	if first.Stdout != second.Stdout {
		t.Fatal("slack manifest is not deterministic")
	}
	if strings.Contains(strings.ToLower(first.Stdout), "token") {
		t.Fatalf("slack manifest carries a credential: %s", first.Stdout)
	}
}

// checkCloudConfig proves the setup section is read back by the same config
// loader the runtime uses, and that no token value reached the file.
func checkCloudConfig(t *testing.T, file string, e *Env) {
	t.Helper()
	cfg, err := fs.ReadFile(os.DirFS(filepath.Dir(file)), filepath.Base(file))
	if err != nil {
		t.Fatal(err)
	}
	text := string(cfg)
	for _, want := range []string{
		"[modules.gateway.whatsapp_cloud]",
		"phone_number_id = \"123456789012345\"",
		"access_token_secret = \"whatsapp_cloud_access_token\"",
		"app_secret_secret = \"whatsapp_cloud_app_secret\"",
		"verify_token_secret = \"whatsapp_cloud_verify_token\"",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config misses %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "EAAG") {
		t.Fatalf("config carries a token value:\n%s", text)
	}
	got := e.Exec("config", "get", "modules.gateway.whatsapp_cloud.phone_number_id")
	got.RequireOK(t)
	if !strings.Contains(got.Stdout, "123456789012345") {
		t.Fatalf("config get did not read the setup back: %s", got.Stdout)
	}
}
