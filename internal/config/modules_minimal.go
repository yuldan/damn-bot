//go:build minimal

package config

// Modules is the compiled-in module configuration of the minimal binary. MCP
// is not compiled in, so [modules.mcp] stays ErrModuleUnavailable (ADR-269).
type Modules struct {
	Gateway ModulesGateway `toml:"gateway"`
}

// defaultModules builds the zero module configuration of this build.
func defaultModules() Modules {
	return Modules{Gateway: ModulesGateway{Telegram: ModulesTelegram{ThreadFallback: "fail"}}}
}

// normalizeMCP is a no-op: no MCP section exists in this build.
func (m *Modules) normalizeMCP() {}

// validateMCP is a no-op: the key never decodes in this build.
func (m *Modules) validateMCP() error { return nil }
