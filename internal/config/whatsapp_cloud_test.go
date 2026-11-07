package config

import (
	"strings"
	"testing"
)

// TestF7WhatsAppCloudConfig proves [modules.gateway.whatsapp_cloud] is parsed
// and validated: an enabled section needs a numeric phone-number id, every
// credential is a lowercase secret name, and api_version looks like v21.0.
func TestF7WhatsAppCloudConfig(t *testing.T) {
	t.Parallel()
	const good = `
[modules.gateway.whatsapp_cloud]
enabled = true
phone_number_id = "123456789012345"
api_version = "v21.0"
access_token_secret = "whatsapp_cloud_access_token"
app_secret_secret = "whatsapp_cloud_app_secret"
verify_token_secret = "whatsapp_cloud_verify_token"
`
	cfg, err := parse(t, good)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := cfg.Modules.Gateway.WhatsAppCloud
	if !got.Enabled || got.PhoneNumberID != "123456789012345" || got.APIVersion != "v21.0" {
		t.Fatalf("section = %+v", got)
	}
	if got.AccessTokenSecret != "whatsapp_cloud_access_token" || got.AppSecretSecret != "whatsapp_cloud_app_secret" ||
		got.VerifyTokenSecret != "whatsapp_cloud_verify_token" {
		t.Fatalf("secret refs = %+v", got)
	}

	for name, tc := range map[string]struct{ toml, key string }{
		"enabled without id": {`
[modules.gateway.whatsapp_cloud]
enabled = true
`, "modules.gateway.whatsapp_cloud.phone_number_id"},
		"non numeric id": {`
[modules.gateway.whatsapp_cloud]
enabled = true
phone_number_id = "123-456"
`, "modules.gateway.whatsapp_cloud.phone_number_id"},
		"bad secret name": {`
[modules.gateway.whatsapp_cloud]
access_token_secret = "Bad Name"
`, "modules.gateway.whatsapp_cloud.access_token_secret"},
		"bad api version": {`
[modules.gateway.whatsapp_cloud]
api_version = "21"
`, "modules.gateway.whatsapp_cloud.api_version"},
	} {
		_, err := parse(t, tc.toml)
		if err == nil || !strings.Contains(err.Error(), tc.key) {
			t.Errorf("%s: err = %v, want key %s", name, err, tc.key)
		}
	}
}
