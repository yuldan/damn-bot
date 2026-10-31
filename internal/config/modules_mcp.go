//go:build !minimal

package config

import (
	"net/url"
	"strings"
)

// Modules is the compiled-in module configuration. Only keys owned by an
// implemented module are accepted here; other [modules] keys stay
// ErrModuleUnavailable. The MCP section exists only in the full build
// (ADR-269): the minimal binary never accepts a key it cannot run.
type Modules struct {
	Gateway ModulesGateway `toml:"gateway"`
	MCP     ModulesMCP     `toml:"mcp"`
}

// ModulesMCP is [modules.mcp]: the client-wide opt-in and one section per
// server. An absent section means no server, never a default server.
type ModulesMCP struct {
	AllowSampling bool                        `toml:"allow_sampling"`
	Servers       map[string]ModulesMCPServer `toml:"servers"`
}

// ModulesMCPServer is [modules.mcp.servers.<name>]. Command (stdio) and URL
// (Streamable HTTP) are exclusive; the trust default is untrusted.
type ModulesMCPServer struct {
	Command  []string                  `toml:"command"`
	Args     []string                  `toml:"args"`
	Env      map[string]string         `toml:"env"`
	URL      string                    `toml:"url"`
	Headers  map[string]string         `toml:"headers"`
	Trust    string                    `toml:"trust"`
	TimeoutS int                       `toml:"timeout_s"`
	Tools    map[string]ModulesMCPTool `toml:"tools"`
}

// ModulesMCPTool is [modules.mcp.servers.<name>.tools.<tool>]: the only
// override that can lower a tool from non-idempotent to read-only.
type ModulesMCPTool struct {
	ReadOnly bool `toml:"read_only"`
}

// defaultModules builds the zero module configuration of this build.
func defaultModules() Modules {
	return Modules{
		Gateway: ModulesGateway{Telegram: ModulesTelegram{ThreadFallback: "fail"}},
	}
}

// normalizeMCP applies the fail-closed defaults: untrusted and 120 s.
func (m *Modules) normalizeMCP() {
	for name, server := range m.MCP.Servers {
		if server.Trust == "" {
			server.Trust = "untrusted"
		}
		if server.TimeoutS == 0 {
			server.TimeoutS = 120
		}
		m.MCP.Servers[name] = server
	}
}

// validateMCP enforces the strict [modules.mcp] schema.
func (m *Modules) validateMCP() error {
	if len(m.MCP.Servers) > 64 {
		return invalid("modules.mcp.servers", "must have at most 64 servers")
	}
	for name, server := range m.MCP.Servers {
		key := "modules.mcp.servers." + name
		if !validProviderName(name) || len(name) > 48 {
			return invalid(key, "server name must match ^[a-z0-9][a-z0-9_-]{0,47}$")
		}
		if err := server.validate(key); err != nil {
			return err
		}
	}
	return nil
}

func (s *ModulesMCPServer) validate(key string) error {
	stdio, remote := len(s.Command) > 0, s.URL != ""
	if stdio == remote {
		return invalid(key, "needs exactly one of command or url")
	}
	if stdio {
		if err := s.validateStdio(key); err != nil {
			return err
		}
	} else if err := s.validateHTTP(key); err != nil {
		return err
	}
	switch s.Trust {
	case "untrusted", "full":
	default:
		return invalid(key+".trust", "must be untrusted or full")
	}
	if s.TimeoutS < 1 || s.TimeoutS > 600 {
		return invalid(key+".timeout_s", "must be between 1 and 600")
	}
	for tool := range s.Tools {
		if tool == "" || len(tool) > 128 || strings.ContainsAny(tool, "\x00\r\n") {
			return invalid(key+".tools", "tool name %q is invalid", tool)
		}
	}
	return nil
}

func (s *ModulesMCPServer) validateStdio(key string) error {
	argv := append(append([]string{}, s.Command...), s.Args...)
	if len(argv) > 64 {
		return invalid(key+".command", "argv has more than 64 entries")
	}
	for i, arg := range argv {
		if arg == "" || len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return invalid(key+".command", "argv[%d] is invalid", i)
		}
	}
	for name, value := range s.Env {
		if !validEnvName(name) {
			return invalid(key+".env", "variable %q is invalid", name)
		}
		if len(value) > 4096 || strings.ContainsRune(value, 0) {
			return invalid(key+".env."+name, "value is invalid")
		}
		if strings.HasPrefix(value, "keychain:") && !validKeyRef(value) {
			return invalid(key+".env."+name, "must be a keychain:<id> reference or a literal value")
		}
	}
	return nil
}

func (s *ModulesMCPServer) validateHTTP(key string) error {
	u, err := url.Parse(s.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") ||
		u.Host == "" || u.User != nil || u.Fragment != "" {
		return invalid(key+".url", "must be an http(s) URL without credentials or fragment")
	}
	if len(s.Command) > 0 || len(s.Args) > 0 || len(s.Env) > 0 {
		return invalid(key, "http servers take url/headers, not command/args/env")
	}
	for name, value := range s.Headers {
		if !validHeaderName(name) || reservedMCPHeader(name) {
			return invalid(key+".headers", "header %q is invalid or transport-owned", name)
		}
		if !validKeyRef(value) {
			return invalid(key+".headers."+name, "must be a keychain:<id> reference, never a plaintext secret")
		}
	}
	return nil
}

// reservedMCPHeader refuses headers the client installs itself.
func reservedMCPHeader(name string) bool {
	switch strings.ToLower(name) {
	case "content-type", "accept", "mcp-session-id", "mcp-protocol-version":
		return true
	}
	return false
}
