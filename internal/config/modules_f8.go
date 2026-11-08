//go:build !minimal

package config

// ModulesConnectors is [modules.connectors] (SPEC §9.9): the F8 connector
// module. Every connector owns its own sub-section, added by the slice that
// implements it; this core only carries the module switch and hard limits.
type ModulesConnectors struct {
	Enabled          bool  `toml:"enabled"`
	TimeoutS         int   `toml:"timeout_s"`
	MaxRetries       int   `toml:"max_retries"`
	MaxResponseBytes int64 `toml:"max_response_bytes"`
}

// validateF8 keeps the module-level limits inside the framework bounds.
func (m *Modules) validateF8() error {
	c := m.Connectors
	if c.TimeoutS < 0 || c.TimeoutS > 120 {
		return invalid("modules.connectors.timeout_s", "must be 1..120")
	}
	if c.MaxRetries < 0 || c.MaxRetries > 3 {
		return invalid("modules.connectors.max_retries", "must be 0..3")
	}
	if c.MaxResponseBytes < 0 || c.MaxResponseBytes > 8<<20 {
		return invalid("modules.connectors.max_response_bytes", "must be 1..8388608")
	}
	return nil
}
