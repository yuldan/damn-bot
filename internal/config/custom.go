//go:build minimal

package config

//line config.go:221

// CustomProvider is one PROVIDERS.md custom provider (12 fields). The
// minimal build has no credential pool, so `credentials` is an unknown key
// (ErrUnknownKey) rather than a silently ignored field.
type CustomProvider struct {
	Name                  string            `toml:"name"`
	BaseURL               string            `toml:"base_url"`
	API                   string            `toml:"api"`
	APIKeyEnv             string            `toml:"api_key_env"`
	APIKey                string            `toml:"api_key"`
	KeyCmd                []string          `toml:"key_cmd"`
	Models                []CustomModel     `toml:"models"`
	ExtraHeaders          map[string]string `toml:"extra_headers"`
	ExtraBody             map[string]any    `toml:"extra_body"`
	TLS                   *CustomTLS        `toml:"tls"`
	SessionAffinityHeader string            `toml:"session_affinity_header"`
	Streaming             *bool             `toml:"streaming"`
	Override              bool              `toml:"override" json:"override"`
}
