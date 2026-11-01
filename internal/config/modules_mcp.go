//go:build !minimal

package config

import (
	"net"
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
	OAuth    *ModulesMCPOAuth          `toml:"oauth"`
	Trust    string                    `toml:"trust"`
	TimeoutS int                       `toml:"timeout_s"`
	Tools    map[string]ModulesMCPTool `toml:"tools"`
}

// ModulesMCPOAuth is [modules.mcp.servers.<name>.oauth] (F5-11). The issuer
// must be HTTPS or loopback; scopes are explicit and minimal; a client secret
// is only ever a keychain reference, never a plaintext credential. Access and
// refresh tokens never live in config.
type ModulesMCPOAuth struct {
	Issuer       string   `toml:"issuer"`
	ClientID     string   `toml:"client_id"`
	ClientSecret string   `toml:"client_secret"`
	Scopes       []string `toml:"scopes"`
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
	if s.OAuth != nil {
		return invalid(key+".oauth", "oauth needs an http server")
	}
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
	if err := s.validateHeaders(key); err != nil {
		return err
	}
	return s.validateOAuth(key)
}

// validateHeaders refuses transport-owned headers and plaintext credentials.
func (s *ModulesMCPServer) validateHeaders(key string) error {
	for name, value := range s.Headers {
		if !validHeaderName(name) || reservedMCPHeader(name) {
			return invalid(key+".headers", "header %q is invalid or transport-owned", name)
		}
		if !validKeyRef(value) {
			return invalid(key+".headers."+name, "must be a keychain:<id> reference, never a plaintext secret")
		}
		if s.OAuth != nil && strings.EqualFold(name, "authorization") {
			return invalid(key+".oauth", "oauth and an Authorization header are exclusive")
		}
	}
	return nil
}

// validateOAuth mirrors internal/mcp: an OAuth section on a stdio server, a
// non-TLS issuer, an implicit scope set, or a plaintext client secret is
// refused at load time.
func (s *ModulesMCPServer) validateOAuth(key string) error {
	o := s.OAuth
	if o == nil {
		return nil
	}
	if s.URL == "" {
		return invalid(key+".oauth", "oauth needs an http server")
	}
	u, err := url.Parse(o.Issuer)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" ||
		(u.Scheme != "https" && (u.Scheme != "http" || !loopbackHost(u.Hostname()))) {
		return invalid(key+".oauth.issuer", "must be an https URL or a loopback authorization server")
	}
	if len(o.ClientID) > 256 || strings.ContainsAny(o.ClientID, "\x00\r\n") {
		return invalid(key+".oauth.client_id", "is invalid")
	}
	if o.ClientSecret != "" && !validKeyRef(o.ClientSecret) {
		return invalid(key+".oauth.client_secret", "must be a keychain:<id> reference, never a plaintext secret")
	}
	return validOAuthScopes(key, o.Scopes)
}

// validOAuthScopes checks the explicit, duplicate-free scope list.
func validOAuthScopes(key string, scopes []string) error {
	if len(scopes) == 0 || len(scopes) > 16 {
		return invalid(key+".oauth.scopes", "must list 1..16 explicit scopes")
	}
	seen := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		if len(scope) > 64 || !validOAuthScope(scope) {
			return invalid(key+".oauth.scopes", "scope %q is invalid", scope)
		}
		if seen[scope] {
			return invalid(key+".oauth.scopes", "scope %q is duplicated", scope)
		}
		seen[scope] = true
	}
	return nil
}

// validOAuthScope is the RFC 6749 scope-token grammar without the wildcard.
func validOAuthScope(s string) bool {
	if s == "" || s == "*" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == ':' || c == '-':
		default:
			return false
		}
	}
	return true
}

// loopbackHost reports whether host is a loopback literal or localhost.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// reservedMCPHeader refuses headers the client installs itself.
func reservedMCPHeader(name string) bool {
	switch strings.ToLower(name) {
	case "content-type", "accept", "mcp-session-id", "mcp-protocol-version":
		return true
	}
	return false
}
