//go:build !minimal

package config

// CustomProvider is one PROVIDERS.md custom provider (12 fields). APIKey may
// only be a keychain reference; Streaming nil means true. Credentials, when
// present, replaces api_key_env/api_key/key_cmd with a SPEC §4.1 pool.
type CustomProvider struct {
	Name                  string               `toml:"name"`
	BaseURL               string               `toml:"base_url"`
	API                   string               `toml:"api"`
	APIKeyEnv             string               `toml:"api_key_env"`
	APIKey                string               `toml:"api_key"`
	KeyCmd                []string             `toml:"key_cmd"`
	Credentials           []ProviderCredential `toml:"credentials"`
	Models                []CustomModel        `toml:"models"`
	ExtraHeaders          map[string]string    `toml:"extra_headers"`
	ExtraBody             map[string]any       `toml:"extra_body"`
	TLS                   *CustomTLS           `toml:"tls"`
	SessionAffinityHeader string               `toml:"session_affinity_header"`
	Streaming             *bool                `toml:"streaming"`
	Override              bool                 `toml:"override" json:"override"`
}

// ProviderCredential is one member of a custom provider's credential pool.
// Secret is only a keychain:<id> reference; a plaintext key is rejected and
// the value is never written to config or printed by any command.
type ProviderCredential struct {
	ID     string `toml:"id"`
	Secret string `toml:"secret"`
}
