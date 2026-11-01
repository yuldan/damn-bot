//go:build !minimal

package config

import (
	"errors"
	"strings"
	"testing"
)

func TestConfigProviderCredentials(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, strings.Join([]string{
		"[[providers.custom]]",
		`name = "k"`,
		`base_url = "https://k.example/v1"`,
		`models = [{ id = "m" }]`,
		`credentials = [{ id = "work", secret = "keychain:k-work" }, { id = "home", secret = "keychain:k-home" }]`,
	}, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	creds := cfg.Providers.Custom[0].Credentials
	if len(creds) != 2 || creds[0].ID != "work" || creds[1].Secret != "keychain:k-home" {
		t.Fatalf("credentials = %+v", creds)
	}
	if cfg.Providers.Custom[0].APIKey != "" || cfg.Providers.Custom[0].APIKeyEnv != "" {
		t.Fatalf("pool coexists with a static key: %+v", cfg.Providers.Custom[0])
	}
}

func TestConfigProviderCredentialsReject(t *testing.T) {
	t.Parallel()
	const valid = `name = "k"
base_url = "https://k.example/v1"
models = [{ id = "m" }]
`
	tests := map[string]string{
		"empty":            `credentials = []`,
		"too_many":         `credentials = [` + strings.Repeat(`{ id = "a", secret = "keychain:x" },`, 16) + `{ id = "b", secret = "keychain:x" }]`,
		"missing_id":       `credentials = [{ secret = "keychain:x" }]`,
		"bad_id":           `credentials = [{ id = "a b", secret = "keychain:x" }]`,
		"leading_punct":    `credentials = [{ id = "-a", secret = "keychain:x" }]`,
		"duplicate_id":     `credentials = [{ id = "a", secret = "keychain:x" }, { id = "A", secret = "keychain:y" }]`,
		"plaintext_secret": `credentials = [{ id = "a", secret = "sk-123" }]`,
		"empty_ref":        `credentials = [{ id = "a", secret = "keychain:" }]`,
		"with_api_key":     "credentials = [{ id = \"a\", secret = \"keychain:x\" }]\napi_key = \"keychain:y\"",
		"with_env":         "credentials = [{ id = \"a\", secret = \"keychain:x\" }]\napi_key_env = \"K_KEY\"",
		"with_key_cmd":     "credentials = [{ id = \"a\", secret = \"keychain:x\" }]\nkey_cmd = [\"k\"]",
	}
	for name, extra := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := parse(t, "[[providers.custom]]\n"+valid+extra+"\n")
			if !errors.Is(err, ErrInvalidValue) {
				t.Fatalf("err = %v, want ErrInvalidValue", err)
			}
		})
	}
}
