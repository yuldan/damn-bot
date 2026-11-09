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

// validateF6 is a no-op: the F6 sections never decode in this build (ADR-310).
func (m *Modules) validateF6() error { return nil }

// validateF8 is a no-op: no connector section exists in this build.
func (m *Modules) validateF8() error { return nil }

// validateAuto is a no-op: [modules.auto] never decodes in this build
// (ADR-333); the key stays ErrModuleUnavailable like every other absent module.
func (m *Modules) validateAuto() error { return nil }

// autoModuleAvailable reports that [modules.auto] is not compiled in.
func autoModuleAvailable() bool { return false }
