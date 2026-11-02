//go:build minimal

package config

import "slices"

//line validate.go:353

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
			return invalid(key+".api_key", "must be a keychain:<id> reference, not plaintext")
		}
	}
	if len(p.KeyCmd) > 0 {
		sources++
		if slices.Contains(p.KeyCmd, "") {
			return invalid(key+".key_cmd", "entries must be non-empty")
		}
	}
	if sources > 1 {
		return invalid(key+".api_key", "set only one of api_key_env, api_key, key_cmd")
	}
	return nil
}
