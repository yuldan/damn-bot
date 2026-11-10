//go:build !minimal

package config

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
)

// validateWeb enforces GAP-SEC-07/08: a public bind without allow_remote,
// without TLS, or with a malformed Host allowlist never starts, and a mode
// whose credentials are not named is refused instead of running without auth.
func (c *Config) validateWeb() error {
	w := &c.Web
	switch w.Auth {
	case WebAuthToken, WebAuthOnce, WebAuthBasic, WebAuthOIDC, WebAuthDrain, WebAuthNous:
	default:
		return invalid("web.auth", "must be token, once, basic, oidc, drain or nous")
	}
	local, err := parseWebListen(w.Listen)
	if err != nil {
		return invalid("web.listen", "%v", err)
	}
	if (w.TLSCert == "") != (w.TLSKey == "") {
		return invalid("web.tls_key", "tls_cert and tls_key must be set together")
	}
	for _, ref := range []struct{ key, path string }{
		{"tls_cert", w.TLSCert},
		{"tls_key", w.TLSKey},
	} {
		if ref.path == "" {
			continue
		}
		if !filepath.IsAbs(ref.path) || strings.ContainsAny(ref.path, "\x00\r\n") {
			return invalid("web."+ref.key, "must be an absolute path without control bytes")
		}
	}
	if !local {
		if !w.AllowRemote {
			return invalid("web.listen", "non-loopback bind %q requires web.allow_remote = true", w.Listen)
		}
		if w.TLSCert == "" {
			return invalid("web.allow_remote", "non-loopback bind requires web.tls_cert and web.tls_key")
		}
	}
	for i, host := range w.AllowedHosts {
		key := fmt.Sprintf("web.allowed_hosts[%d]", i)
		if !validAllowedHost(host) {
			return invalid(key, "%q is not a bare host name or IP literal", host)
		}
		if slices.Contains(w.AllowedHosts[:i], host) {
			return invalid(key, "duplicate host %q", host)
		}
	}
	return w.validateAuthMode()
}

// validateAuthMode checks that each mode's credential source is named and
// that a mode does not carry another mode's references.
func (w *Web) validateAuthMode() error {
	basic := w.BasicUser != "" || w.BasicPasswordSecret != ""
	oidc := w.OIDCIssuer != "" || w.OIDCClientID != "" || w.OIDCClientSecretSecret != ""
	if w.Auth != WebAuthBasic && basic {
		return invalid("web.basic_user", "only valid with web.auth = basic")
	}
	if w.Auth != WebAuthOIDC && oidc {
		return invalid("web.oidc_issuer", "only valid with web.auth = oidc")
	}
	switch w.Auth {
	case WebAuthBasic:
		return w.validateBasicAuth()
	case WebAuthOIDC:
		return w.validateOIDCAuth()
	default:
		return nil
	}
}

// validateBasicAuth requires the single principal and its keychain secret.
func (w *Web) validateBasicAuth() error {
	if strings.TrimSpace(w.BasicUser) == "" || strings.ContainsAny(w.BasicUser, ": \t\r\n\x00") {
		return invalid("web.basic_user", "must be a non-empty user name without colon or whitespace")
	}
	if !validKeychainSecretName(w.BasicPasswordSecret) {
		return invalid("web.basic_password_secret", "must be a secret name")
	}
	return nil
}

// validateOIDCAuth requires the issuer, client id and client-secret name.
func (w *Web) validateOIDCAuth() error {
	if !validOIDCIssuer(w.OIDCIssuer) {
		return invalid("web.oidc_issuer", "must be an https URL without userinfo, query or fragment")
	}
	if w.OIDCClientID == "" || len(w.OIDCClientID) > 256 || strings.ContainsAny(w.OIDCClientID, " \t\r\n\x00") {
		return invalid("web.oidc_client_id", "must be a non-empty client id without whitespace")
	}
	if !validKeychainSecretName(w.OIDCClientSecretSecret) {
		return invalid("web.oidc_client_secret_secret", "must be a secret name")
	}
	return nil
}

// parseWebListen reports whether listen is a local-only bind (a loopback
// host:port or a unix socket, whose peer UID the server checks) and rejects
// malformed forms early. A host that cannot be proven loopback counts as
// remote.
func parseWebListen(listen string) (local bool, err error) {
	if raw, ok := strings.CutPrefix(listen, "unix:"); ok {
		// A unix socket path is POSIX-shaped even when the config is parsed on
		// Windows, so accept a leading "/" as absolute regardless of GOOS.
		absolute := strings.HasPrefix(raw, "/") || filepath.IsAbs(raw)
		if _, placeholder := strings.CutPrefix(raw, "<state_dir>/"); !placeholder && !absolute {
			return false, fmt.Errorf("unix socket %q must be absolute or start with <state_dir>/", raw)
		}
		if strings.ContainsAny(raw, "\x00\r\n") || strings.Contains(raw, " ") {
			return false, fmt.Errorf("unix socket path contains invalid bytes")
		}
		return true, nil
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return false, fmt.Errorf("must be host:port or unix:<path>")
	}
	if !validPort(port) {
		return false, fmt.Errorf("port %q must be 1..65535", port)
	}
	if host == "" {
		return false, nil // every interface
	}
	if host == "localhost" {
		return true, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback(), nil
	}
	if !validHostname(host) {
		return false, fmt.Errorf("host %q is not a host name or IP literal", host)
	}
	return false, nil
}

// validPort is a decimal TCP port in 1..65535.
func validPort(s string) bool {
	if s == "" || len(s) > 5 || !digitsOnly(s) {
		return false
	}
	n := 0
	for i := range s {
		n = n*10 + int(s[i]-'0')
	}
	return n >= 1 && n <= 65535
}

// validHostname is the DNS grammar accepted for a bind address: labels of
// letters, digits and inner dashes, at most 253 bytes.
func validHostname(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for label := range strings.SplitSeq(s, ".") {
		if !validHostLabel(label) {
			return false
		}
	}
	return true
}

func validHostLabel(label string) bool {
	if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	return strings.IndexFunc(label, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-'
	}) < 0
}

// validAllowedHost accepts a Host allowlist entry: a host name, an IPv4
// literal or a bracketed-free IPv6 literal, lower case and without any port,
// scheme, path or wildcard.
func validAllowedHost(host string) bool {
	if host == "" || len(host) > 253 || strings.ToLower(host) != host {
		return false
	}
	if strings.ContainsAny(host, "/\\*?@[] \t\r\n\x00") {
		return false
	}
	if strings.Contains(host, ":") {
		return net.ParseIP(host) != nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return true
	}
	return validHostname(host)
}

// validOIDCIssuer is an https URL (http only for a loopback development
// issuer) without userinfo, query or fragment.
func validOIDCIssuer(raw string) bool {
	if !validBaseURL(raw) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}
