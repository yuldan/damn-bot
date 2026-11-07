package whatsapp_cloud_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/gwplat/whatsapp_cloud"
)

// TestF7WhatsAppCloudSetup proves the setup record is 0600 JSON carrying the
// phone-number id and secret names only, never a credential value.
func TestF7WhatsAppCloudSetup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	want := whatsapp_cloud.Setup{
		PhoneNumberID:     "123456789012345",
		APIVersion:        "v21.0",
		AccessTokenSecret: whatsapp_cloud.AccessTokenSecret,
		AppSecretSecret:   whatsapp_cloud.AppSecretSecret,
		VerifyTokenSecret: whatsapp_cloud.VerifyTokenSecret,
	}
	path, err := whatsapp_cloud.SaveSetup(dir, &want)
	if err != nil {
		t.Fatalf("SaveSetup: %v", err)
	}
	if got, wantPath := path, filepath.Join(dir, "gateway", whatsapp_cloud.SetupFile); got != wantPath {
		t.Fatalf("path = %q, want %q", got, wantPath)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	var got whatsapp_cloud.Setup
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != want {
		t.Errorf("record = %+v, want %+v", got, want)
	}
	if strings.Contains(string(data), "EAAG") {
		t.Errorf("record carries a token value: %s", data)
	}

	checkSetupRejects(t, dir)
}

func checkSetupRejects(t *testing.T, dir string) {
	t.Helper()
	good := whatsapp_cloud.Setup{
		PhoneNumberID:     "123456789012345",
		AccessTokenSecret: whatsapp_cloud.AccessTokenSecret,
		AppSecretSecret:   whatsapp_cloud.AppSecretSecret,
		VerifyTokenSecret: whatsapp_cloud.VerifyTokenSecret,
	}
	for name, mutate := range map[string]func(*whatsapp_cloud.Setup){
		"empty phone":    func(s *whatsapp_cloud.Setup) { s.PhoneNumberID = "" },
		"non numeric":    func(s *whatsapp_cloud.Setup) { s.PhoneNumberID = "123-456" },
		"plaintext ref":  func(s *whatsapp_cloud.Setup) { s.AccessTokenSecret = "EAAG-plaintext" },
		"empty ref":      func(s *whatsapp_cloud.Setup) { s.VerifyTokenSecret = "" },
		"uppercase ref":  func(s *whatsapp_cloud.Setup) { s.AppSecretSecret = "App_Secret" },
		"too long ident": func(s *whatsapp_cloud.Setup) { s.PhoneNumberID = strings.Repeat("9", 33) },
	} {
		s := good
		mutate(&s)
		if _, err := whatsapp_cloud.SaveSetup(dir, &s); err == nil {
			t.Errorf("%s: SaveSetup accepted %+v", name, s)
		}
	}
}

// TestF7WhatsAppCloudSetupSecrets keeps the setup constants identical to the
// descriptor's declared secret names: one source of truth for the keychain.
func TestF7WhatsAppCloudSetupSecrets(t *testing.T) {
	t.Parallel()
	var declared []string
	for _, d := range gwplat.Descriptors() {
		if d.Name == "whatsapp_cloud" {
			declared = d.Secrets
		}
	}
	if len(declared) == 0 {
		t.Fatal("whatsapp_cloud descriptor declares no secrets")
	}
	want := []string{whatsapp_cloud.AccessTokenSecret, whatsapp_cloud.AppSecretSecret, whatsapp_cloud.VerifyTokenSecret}
	for _, name := range want {
		if !slices.Contains(declared, name) {
			t.Errorf("setup secret %q is not declared by the descriptor: %v", name, declared)
		}
	}
}
