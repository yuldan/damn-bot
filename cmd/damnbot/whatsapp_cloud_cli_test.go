//go:build !minimal

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/gwplat/whatsapp_cloud"
)

// TestF7CLI016WhatsAppCloud proves the Cloud API setup (CLI-016): the
// phone-number id and the three keychain names are written to
// [modules.gateway.whatsapp_cloud], the token is only ever named, the config
// loader reads the section back, and the Meta webhook verification contract is
// printed without a credential.
func TestF7CLI016WhatsAppCloud(t *testing.T) {
	file, _ := configTestHome(t, "[provider]\nmodel = \"openai/gpt-test\"\n")

	code, stdout, stderr := runArgs(t, "whatsapp-cloud", "setup",
		"--phone-number-id", "123456789012345")
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

	text := readText(t, file)
	if !strings.Contains(text, waCloudSection) {
		t.Fatalf("config has no %s:\n%s", waCloudSection, text)
	}
	if strings.Contains(text, "EAAG") {
		t.Fatalf("config carries a token value:\n%s", text)
	}
	loaded := checkWhatsAppCloudLoad(t, file)
	if loaded.APIVersion != "" {
		t.Errorf("api version = %q, want the adapter default", loaded.APIVersion)
	}

	// A second setup is idempotent and leaves the file byte-identical.
	staged, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runArgs(t, "whatsapp-cloud", "setup", "--phone-number-id", "123456789012345"); code != 0 {
		t.Fatalf("second setup = %d (%s)", code, stderr)
	}
	again, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, staged) {
		t.Fatalf("second setup changed the config:\n%s\n---\n%s", staged, again)
	}

	checkWhatsAppCloudRejects(t, file, staged)
}

// checkWhatsAppCloudLoad reads the written section through the production
// config loader (not the CLI's in-memory copy): the reader is config.Load.
func checkWhatsAppCloudLoad(t *testing.T, file string) config.ModulesWhatsAppCloud {
	t.Helper()
	loader, err := config.New(config.OS())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loader.Load(file)
	if err != nil {
		t.Fatalf("config loader rejected the setup: %v", err)
	}
	cloud := cfg.Modules.Gateway.WhatsAppCloud
	if !cloud.Enabled || cloud.PhoneNumberID != "123456789012345" {
		t.Fatalf("loaded section = %+v", cloud)
	}
	if cloud.AccessTokenSecret != whatsapp_cloud.AccessTokenSecret ||
		cloud.AppSecretSecret != whatsapp_cloud.AppSecretSecret ||
		cloud.VerifyTokenSecret != whatsapp_cloud.VerifyTokenSecret {
		t.Fatalf("loaded secret refs = %+v", cloud)
	}
	return cloud
}

func checkWhatsAppCloudRejects(t *testing.T, file string, before []byte) {
	t.Helper()
	unchanged := func(name string) {
		t.Helper()
		now, err := os.ReadFile(filepath.Clean(file))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(now, before) {
			t.Fatalf("%s wrote the config:\n%s", name, now)
		}
	}
	if code, _, _ := runArgs(t, "whatsapp-cloud", "setup",
		"--phone-number-id", "123456789012345", "--access-token", "EAAG-plaintext"); code == 0 {
		t.Fatal("setup accepted a plaintext --access-token flag")
	}
	unchanged("plaintext flag")
	if code, _, stderr := runArgs(t, "whatsapp-cloud", "setup", "--phone-number-id", "not-a-number"); code == 0 ||
		!strings.Contains(stderr, "phone_number_id") {
		t.Fatalf("bad phone-number id = %d (%s)", code, stderr)
	}
	unchanged("bad phone-number id")
	if code, _, _ := runArgs(t, "whatsapp-cloud", "setup",
		"--phone-number-id", "123456789012345", "--access-token-secret", "Bad Name"); code == 0 {
		t.Fatal("setup accepted an invalid secret name")
	}
	unchanged("invalid secret name")
}
