//go:build !minimal

package config

import (
	"slices"
	"strconv"
	"strings"
)

func validateProviderAuth(p *CustomProvider, key string) error {
	sources := 0
	if p.APIKeyEnv != "" {
		sources++
		if !validKeyEnvName(p.APIKeyEnv) {
			return invalid(key+".api_key_env", "must match ^[A-Z_][A-Z0-9_]*$")
		}
	}
	if p.APIKey != "" {
		sources++
		if !validKeyRef(p.APIKey) {
			return invalid(key+".api_key", "must be a keychain:<id> reference, never a plaintext key")
		}
	}
	if len(p.KeyCmd) > 0 {
		sources++
		if slices.Contains(p.KeyCmd, "") {
			return invalid(key+".key_cmd", "entries must be non-empty")
		}
	}
	if p.Credentials != nil {
		sources++
		if err := validateProviderCredentials(p.Credentials, key); err != nil {
			return err
		}
	}
	if sources > 1 {
		return invalid(key+".api_key", "at most one of api_key_env, api_key, key_cmd and credentials may be set")
	}
	return nil
}

// maxProviderCredentials bounds one pool so rotation attempts stay bounded
// (the provider pool uses the same limit).
const maxProviderCredentials = 16

// validateProviderCredentials enforces SPEC §4.1: 1..16 entries, a unique
// stable id per entry, and keychain:<id> references only.
func validateProviderCredentials(creds []ProviderCredential, key string) error {
	if len(creds) == 0 {
		return invalid(key+".credentials", "must list at least one credential")
	}
	if len(creds) > maxProviderCredentials {
		return invalid(key+".credentials", "at most %d credentials, got %d", maxProviderCredentials, len(creds))
	}
	seen := make(map[string]struct{}, len(creds))
	for j, c := range creds {
		ck := key + ".credentials[" + strconv.Itoa(j) + "]"
		if !validCredentialID(c.ID) {
			return invalid(ck+".id", "must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
		}
		if _, dup := seen[strings.ToLower(c.ID)]; dup {
			return invalid(ck+".id", "duplicate credential id %q", c.ID)
		}
		seen[strings.ToLower(c.ID)] = struct{}{}
		if !validKeyRef(c.Secret) {
			return invalid(ck+".secret", "must be a keychain:<id> reference, never a plaintext key")
		}
	}
	return nil
}

// validCredentialID is the stable pool id grammar: it reaches events, the
// ledger, and doctor output, so it is more restrictive than an arbitrary key.
func validCredentialID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := range len(s) {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		case i > 0 && (ch == '.' || ch == '_' || ch == '-'):
		default:
			return false
		}
	}
	return true
}
