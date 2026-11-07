package e2e

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// whatsappCloudRecord mirrors the CLI-016 setup record, decoded here without
// importing the adapter package (the e2e harness stays on the binary surface).
type whatsappCloudRecord struct {
	PhoneNumberID     string `json:"phone_number_id"`
	AccessTokenSecret string `json:"access_token_secret"`
	AppSecretSecret   string `json:"app_secret_secret"`
	VerifyTokenSecret string `json:"verify_token_secret"`
}

// TestF7E2EAdapterCLI drives the three adapter setup surfaces on the real
// binary (CLI-015/016/017): the Cloud API is the only offered mode, the Web
// bridge needs `--accept-ban-risk`, the Cloud setup writes secret names only,
// and the Slack manifest is deterministic and credential-free.
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

	cloudDir := filepath.Join(t.TempDir(), "cloud")
	cloud := e.Exec("whatsapp-cloud", "setup", "--phone-number-id", "123456789012345", "--data-dir", cloudDir)
	cloud.RequireOK(t)
	if !strings.Contains(cloud.Stdout, "hub.verify_token") || !strings.Contains(cloud.Stdout, "X-Hub-Signature-256") {
		t.Fatalf("webhook verification missing: %s", cloud.Stdout)
	}
	checkCloudRecord(t, filepath.Join(cloudDir, "gateway", "whatsapp_cloud.json"))

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

func checkCloudRecord(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	var record whatsappCloudRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.PhoneNumberID != "123456789012345" {
		t.Errorf("phone-number id = %q", record.PhoneNumberID)
	}
	if record.AccessTokenSecret != "whatsapp_cloud_access_token" ||
		record.AppSecretSecret != "whatsapp_cloud_app_secret" ||
		record.VerifyTokenSecret != "whatsapp_cloud_verify_token" {
		t.Errorf("secret refs = %+v", record)
	}
	if strings.Contains(string(data), "EAAG") {
		t.Fatalf("setup record carries a token: %s", data)
	}
}
