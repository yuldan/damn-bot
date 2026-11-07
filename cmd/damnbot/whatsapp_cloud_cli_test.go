//go:build !minimal

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/gwplat/whatsapp_cloud"
)

// TestF7CLI016WhatsAppCloud proves the Cloud API setup (CLI-016): the
// phone-number id and the three keychain names are written to the setup record,
// the token is only ever named, and the Meta webhook verification contract is
// printed without a credential.
func TestF7CLI016WhatsAppCloud(t *testing.T) {
	configTestHome(t, "[provider]\nmodel = \"openai/gpt-test\"\n")
	dataDir := t.TempDir()

	code, stdout, stderr := runArgs(t, "whatsapp-cloud", "setup",
		"--phone-number-id", "123456789012345", "--data-dir", dataDir)
	if code != 0 {
		t.Fatalf("setup = %d (%s)", code, stderr)
	}
	for _, want := range []string{"hub.verify_token", "X-Hub-Signature-256", whatsapp_cloud.AccessTokenSecret} {
		if !strings.Contains(stdout, want) {
			t.Errorf("setup output misses %q: %s", want, stdout)
		}
	}
	if strings.Contains(strings.ToLower(stdout), "bearer") || strings.Contains(stdout, "EAAG") {
		t.Fatalf("setup output looks like a token: %s", stdout)
	}

	path := filepath.Join(dataDir, "gateway", whatsapp_cloud.SetupFile)
	first := checkWhatsAppCloudRecord(t, path)
	if first.APIVersion != "" {
		t.Errorf("api version = %q, want the adapter default", first.APIVersion)
	}

	// A second setup is idempotent and leaves the record byte-identical.
	staged, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runArgs(t, "whatsapp-cloud", "setup",
		"--phone-number-id", first.PhoneNumberID, "--data-dir", dataDir)
	if code != 0 {
		t.Fatalf("second setup = %d (%s)", code, stderr)
	}
	again, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, staged) {
		t.Fatalf("second setup changed the record:\n%s\n---\n%s", staged, again)
	}

	checkWhatsAppCloudRejectsPlaintext(t, dataDir)
	checkWhatsAppCloudRejectsBadID(t, dataDir)
}

// checkWhatsAppCloudRecord decodes and validates the written setup.
func checkWhatsAppCloudRecord(t *testing.T, path string) whatsapp_cloud.Setup {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("setup record: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("setup record mode = %o, want 600", mode)
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read setup record: %v", err)
	}
	var setup whatsapp_cloud.Setup
	if err := json.Unmarshal(data, &setup); err != nil {
		t.Fatalf("decode setup record: %v", err)
	}
	if setup.PhoneNumberID != "123456789012345" {
		t.Errorf("phone-number id = %q", setup.PhoneNumberID)
	}
	if setup.AccessTokenSecret != whatsapp_cloud.AccessTokenSecret ||
		setup.AppSecretSecret != whatsapp_cloud.AppSecretSecret ||
		setup.VerifyTokenSecret != whatsapp_cloud.VerifyTokenSecret {
		t.Errorf("secret refs = %+v", setup)
	}
	if err := setup.Validate(); err != nil {
		t.Errorf("written setup does not validate: %v", err)
	}
	return setup
}

func checkWhatsAppCloudRejectsPlaintext(t *testing.T, dataDir string) {
	t.Helper()
	code, _, _ := runArgs(t, "whatsapp-cloud", "setup",
		"--phone-number-id", "123456789012345", "--access-token", "EAAG-plaintext", "--data-dir", dataDir)
	if code == 0 {
		t.Fatal("setup accepted a plaintext --access-token flag")
	}
}

func checkWhatsAppCloudRejectsBadID(t *testing.T, dataDir string) {
	t.Helper()
	code, _, stderr := runArgs(t, "whatsapp-cloud", "setup",
		"--phone-number-id", "not-a-number", "--data-dir", dataDir)
	if code == 0 || !strings.Contains(stderr, "phone-number id") {
		t.Fatalf("bad phone-number id = %d (%s)", code, stderr)
	}
}
