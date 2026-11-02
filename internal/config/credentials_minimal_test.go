//go:build minimal

package config

import (
	"errors"
	"testing"
)

func TestMinimalRejectsCredentials(t *testing.T) {
	t.Parallel()
	_, err := parse(t, "[[providers.custom]]\nname = \"k\"\nbase_url = \"https://k.example/v1\"\nmodels = [{ id = \"m\" }]\ncredentials = [{ id = \"a\", secret = \"keychain:x\" }]\n")
	ke := keyErrorOf(t, err, ErrUnknownKey)
	if ke.Key != "providers.custom.credentials" {
		t.Fatalf("key = %q (err %v)", ke.Key, err)
	}
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v", err)
	}
}
