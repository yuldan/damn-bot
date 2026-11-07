package whatsapp_cloud_test

import (
	"slices"
	"testing"

	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/gwplat/whatsapp_cloud"
)

// TestF7WhatsAppCloudSetupSecrets keeps the CLI setup constants identical to
// the descriptor's declared secret names: one source of truth for the keychain.
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
	for _, name := range []string{
		whatsapp_cloud.AccessTokenSecret,
		whatsapp_cloud.AppSecretSecret,
		whatsapp_cloud.VerifyTokenSecret,
	} {
		if !slices.Contains(declared, name) {
			t.Errorf("setup secret %q is not declared by the descriptor: %v", name, declared)
		}
	}
}
