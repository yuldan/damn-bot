package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// OAuth is [modules.mcp.servers.<name>.oauth]: the authorization server of
// one Streamable HTTP server (SPEC §9.6, F5-11). The credential itself never
// lives in config; only the issuer, the optional client id from dynamic
// registration, and the exact scopes this client asks for.
type OAuth struct {
	Issuer   string   `json:"issuer"`
	ClientID string   `json:"client_id,omitempty"`
	Scopes   []string `json:"scopes"`
}

// OAuth limits keep a server section from turning into an unbounded token
// request. The set of scopes is explicit: an empty list is invalid rather
// than an implicit broad default.
const (
	maxOAuthScopes    = 16
	maxOAuthScopeLen  = 64
	maxOAuthClientID  = 256
	oauthScopePattern = `^[A-Za-z0-9][A-Za-z0-9._:-]*$`
)

var (
	// ErrAuthRequired is a server that needs consent before it can be used.
	ErrAuthRequired = errors.New("mcp: authorization required")
	// ErrUnauthorized is a server that rejected the credential.
	ErrUnauthorized = errors.New("mcp: unauthorized")
	// ErrScope is a granted scope outside the minimal requested set.
	ErrScope = errors.New("mcp: scope not granted")
)

var scopeRE = regexp.MustCompile(oauthScopePattern)

// TokenSource is the composition root's OAuth adapter. internal/mcp cannot
// import provider/auth (E-06), so it asks for the bearer token through this
// two-method seam. Implementations persist and refresh the token; the client
// only ever receives the access token string and never logs it.
type TokenSource interface {
	// AccessToken returns a usable bearer token, refreshing an expired one.
	AccessToken(ctx context.Context) (string, error)
	// RefreshToken forces a refresh after a 401 and returns the new token.
	RefreshToken(ctx context.Context) (string, error)
}

// Validate rejects an OAuth section that cannot satisfy the transport rule:
// the issuer must be HTTPS or loopback, the scopes explicit and minimal, and
// the client id printable.
func (o *OAuth) Validate(server string) error {
	if o == nil {
		return nil
	}
	if err := endpointOK(o.Issuer); err != nil {
		return fmt.Errorf("%w: %s oauth issuer: %w", ErrInvalidConfig, server, err)
	}
	if len(o.ClientID) > maxOAuthClientID || strings.ContainsAny(o.ClientID, "\x00\r\n") {
		return fmt.Errorf("%w: %s oauth client_id", ErrInvalidConfig, server)
	}
	if len(o.Scopes) == 0 || len(o.Scopes) > maxOAuthScopes {
		return fmt.Errorf("%w: %s oauth scopes must list 1..%d entries", ErrInvalidConfig, server, maxOAuthScopes)
	}
	seen := make(map[string]bool, len(o.Scopes))
	for _, s := range o.Scopes {
		if len(s) > maxOAuthScopeLen || !scopeRE.MatchString(s) || s == "*" {
			return fmt.Errorf("%w: %s oauth scope %q", ErrInvalidConfig, server, s)
		}
		if seen[s] {
			return fmt.Errorf("%w: %s duplicate oauth scope %q", ErrInvalidConfig, server, s)
		}
		seen[s] = true
	}
	return nil
}

// scopes returns the requested scope set.
func (o *OAuth) scopes() map[string]bool {
	out := make(map[string]bool, len(o.Scopes))
	if o == nil {
		return out
	}
	for _, s := range o.Scopes {
		out[s] = true
	}
	return out
}

// CheckScope refuses a token that carries a scope this client did not ask
// for: an authorization server may not widen an explicitly minimal request.
func (o *OAuth) CheckScope(granted string) error {
	allowed := o.scopes()
	for s := range strings.FieldsSeq(granted) {
		if !allowed[s] {
			return fmt.Errorf("%w: %s", ErrScope, s)
		}
	}
	return nil
}

// endpointOK is the transport rule of provider/auth applied to a module: a
// credential may only travel over TLS, or to loopback for a local AS.
func endpointOK(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return fmt.Errorf("%w: issuer", ErrInvalidConfig)
	}
	switch {
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && isLoopback(u.Hostname()):
		return nil
	}
	return fmt.Errorf("%w: issuer must be https or loopback", ErrInvalidConfig)
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
