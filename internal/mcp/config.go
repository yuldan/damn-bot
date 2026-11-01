package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Trust is the operator's explicit trust level for one server (GAP-SEC-03).
// The zero value is untrusted; only the literal "full" raises it, and even a
// trusted server never receives a lower tool effect.
type Trust string

// Trust levels.
const (
	// TrustUntrusted wraps every result as untrusted and isolates the server.
	TrustUntrusted Trust = "untrusted"
	// TrustFull is the explicit opt-in; it still requires approval for
	// non-read tools.
	TrustFull Trust = "full"
)

// Defaults for one server. A server timeout never exceeds one call budget;
// the init handshake has its own shorter deadline.
const (
	DefaultTimeoutS = 120
	MaxTimeoutS     = 600
	MaxServers      = 64
	MaxServerName   = 48
	MaxArgv         = 64
	MaxConfigField  = 4096
)

// Server is one MCP server section. Command and URL are exclusive; a stdio
// server always runs with the environment built by the host Secrets seam.
type Server struct {
	Name     string            `json:"name"`
	Command  []string          `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	URL      string            `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Trust    Trust             `json:"trust,omitempty"`
	TimeoutS int               `json:"timeout_s,omitempty"`
	// OAuth authorizes a Streamable HTTP server (F5-11). It is mutually
	// exclusive with a static Authorization header: config can not hold two
	// credentials for one transport.
	OAuth *OAuth `json:"oauth,omitempty"`
	// ReadOnly is the per-tool read-only allowlist. An absent tool is
	// non-idempotent: a server hint never lowers the gate (GAP-SEC-03).
	ReadOnly map[string]bool `json:"read_only,omitempty"`
}

// Normalize applies the fail-closed defaults: untrusted and 120 s.
func (s *Server) Normalize() {
	if s.Trust == "" {
		s.Trust = TrustUntrusted
	}
	if s.TimeoutS == 0 {
		s.TimeoutS = DefaultTimeoutS
	}
}

// Validate rejects a malformed server before any child process or request.
func (s *Server) Validate() error {
	if !validServerName(s.Name) {
		return fmt.Errorf("%w: server name %q", ErrInvalidConfig, s.Name)
	}
	stdio, remote := len(s.Command) > 0, s.URL != ""
	if stdio == remote {
		return fmt.Errorf("%w: %s needs exactly one of command or url", ErrInvalidConfig, s.Name)
	}
	if stdio {
		if err := s.validateCommand(); err != nil {
			return err
		}
	} else if err := s.validateHTTP(); err != nil {
		return err
	}
	if s.Trust != TrustUntrusted && s.Trust != TrustFull {
		return fmt.Errorf("%w: %s trust %q", ErrInvalidConfig, s.Name, s.Trust)
	}
	if s.TimeoutS < 1 || s.TimeoutS > MaxTimeoutS {
		return fmt.Errorf("%w: %s timeout_s %d", ErrInvalidConfig, s.Name, s.TimeoutS)
	}
	for tool := range s.ReadOnly {
		if tool == "" || len(tool) > 128 || strings.ContainsAny(tool, "\x00\r\n") {
			return fmt.Errorf("%w: %s tool %q", ErrInvalidConfig, s.Name, tool)
		}
	}
	return nil
}

func (s *Server) validateCommand() error {
	if s.OAuth != nil {
		return fmt.Errorf("%w: %s oauth needs an http server", ErrInvalidConfig, s.Name)
	}
	argv := append(append([]string{}, s.Command...), s.Args...)
	if len(argv) > MaxArgv {
		return fmt.Errorf("%w: %s has %d argv entries", ErrInvalidConfig, s.Name, len(argv))
	}
	for i, arg := range argv {
		if arg == "" || len(arg) > MaxConfigField || strings.ContainsRune(arg, 0) {
			return fmt.Errorf("%w: %s argv[%d]", ErrInvalidConfig, s.Name, i)
		}
	}
	for name, value := range s.Env {
		if !validEnvName(name) || !validEnvValue(value) {
			return fmt.Errorf("%w: %s env %q", ErrInvalidConfig, s.Name, name)
		}
	}
	return nil
}

func (s *Server) validateHTTP() error {
	u, err := url.Parse(s.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.Fragment != "" {
		return fmt.Errorf("%w: %s url", ErrInvalidConfig, s.Name)
	}
	if len(s.Command) > 0 || len(s.Env) > 0 {
		return fmt.Errorf("%w: %s mixes http and stdio fields", ErrInvalidConfig, s.Name)
	}
	for name, value := range s.Headers {
		if !validHeaderName(name) || reservedHeader(name) {
			return fmt.Errorf("%w: %s header %q", ErrInvalidConfig, s.Name, name)
		}
		if !strings.HasPrefix(value, "keychain:") {
			return fmt.Errorf("%w: %s header %q must be a keychain:<id> reference", ErrInvalidConfig, s.Name, name)
		}
		if s.OAuth != nil && http.CanonicalHeaderKey(name) == "Authorization" {
			return fmt.Errorf("%w: %s oauth and an Authorization header are exclusive", ErrInvalidConfig, s.Name)
		}
	}
	return s.OAuth.Validate(s.Name)
}

// Timeout is the per-call deadline of this server.
func (s *Server) Timeout() time.Duration { return time.Duration(s.TimeoutS) * time.Second }

// ConfigSHA is the cache key component: every behaviour-affecting field is
// canonical JSON, so a changed server never reads another server's cache.
func (s *Server) ConfigSHA() (string, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("mcp: config sha: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// argv returns the executable and arguments passed to host.Spawn.
func (s *Server) argv() []string {
	out := make([]string, 0, len(s.Command)+len(s.Args))
	out = append(out, s.Command...)
	return append(out, s.Args...)
}

func validServerName(name string) bool {
	if name == "" || len(name) > MaxServerName {
		return false
	}
	for i := range len(name) {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

func validEnvName(name string) bool {
	if name == "" || len(name) > 255 || name[0] >= '0' && name[0] <= '9' {
		return false
	}
	for i := range len(name) {
		c := name[i]
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

func validEnvValue(value string) bool {
	return len(value) <= MaxConfigField && !strings.ContainsRune(value, 0)
}

func validHeaderName(name string) bool {
	return name != "" && len(name) <= 128 && httpgutsValid(name)
}

// httpgutsValid is the RFC 7230 token check without importing net/http's
// internal package.
func httpgutsValid(name string) bool {
	const punctuation = "!#$%&'*+-.^_`|~"
	for i := range len(name) {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case strings.ContainsRune(punctuation, rune(c)):
		default:
			return false
		}
	}
	return true
}

// reservedHeader refuses transport-owned headers: the client installs the
// session and protocol version itself and must never be shadowed by config.
func reservedHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Content-Type", "Accept", "Mcp-Session-Id", "Mcp-Protocol-Version":
		return true
	}
	return false
}
