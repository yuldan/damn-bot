package web

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// ContentSecurityPolicy is the exact GAP-SEC-06 header, with one nonce per
// request for both scripts and styles. There is no unsafe-inline/eval and no
// strict-dynamic; report-uri keeps violation reports on the local server.
func ContentSecurityPolicy(nonce string) string {
	return "default-src 'none'; script-src 'self' 'nonce-" + nonce +
		"'; style-src 'self' 'nonce-" + nonce +
		"'; img-src 'self' data: blob:; media-src 'self' blob:; font-src 'self'; connect-src 'self'; " +
		"frame-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; " +
		"frame-ancestors 'none'; report-uri /csp-report"
}

// newNonce returns a 128-bit base64url nonce.
func newNonce() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// setSecurityHeaders stamps every response (GAP-SEC-06).
func setSecurityHeaders(w http.ResponseWriter, nonce string) {
	h := w.Header()
	h.Set("Content-Security-Policy", ContentSecurityPolicy(nonce))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(self)")
	h.Set("Cache-Control", "no-store")
}

// ServeHTTP is the router: headers, Host allowlist, the public auth surface and
// the session API. Pages and assets land in slice 2c.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w, newNonce())
	if !s.hostAllowed(r.Host) {
		http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
		return
	}
	p := r.URL.Path
	switch {
	case p == "/healthz":
		s.healthz(w)
	case s.authRoute(w, r, p):
	case s.apiRoute(w, r, p):
	default:
		http.NotFound(w, r)
	}
}

// authRoute serves the exchange surface; it reports whether it answered.
func (s *Server) authRoute(w http.ResponseWriter, r *http.Request, p string) bool {
	switch p {
	case "/auth/exchange":
		// Before a CSRF token exists only the origin/fetch-metadata half of
		// GAP-SEC-08 applies.
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return true
		}
		if !sameOrigin(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return true
		}
		s.exchange(w, r)
	case "/auth/callback":
		s.callback(w, r)
	case "/csp-report":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return true
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		return false
	}
	return true
}

// apiRoute serves the session API; both endpoints require a session.
func (s *Server) apiRoute(w http.ResponseWriter, r *http.Request, p string) bool {
	switch p {
	case "/api/session":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return true
		}
		s.withSession(w, r, s.sessionInfo)
	case "/api/logout":
		if s.mutation(w, r) {
			s.withSession(w, r, s.logout)
		}
	default:
		return false
	}
	return true
}

// mutation guards a state-changing request: same-origin fetch metadata plus the
// double-submit token (GAP-SEC-08).
func (s *Server) mutation(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return false
	}
	if !sameOrigin(r) || !doubleSubmit(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// withSession runs h only for an authenticated request; a missing or expired
// session is a bare 401 with no token material.
func (s *Server) withSession(w http.ResponseWriter, r *http.Request, h func(http.ResponseWriter, *http.Request, *Session)) {
	row, ok := s.authenticate(w, r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	h(w, r, &row)
}

// hostAllowed is the anti-DNS-rebinding check (GAP-SEC-08).
func (s *Server) hostAllowed(hostport string) bool {
	if hostport == "" {
		return true
	}
	host := hostport
	if u, err := url.Parse("//" + hostport); err == nil {
		host = u.Hostname()
	}
	if local, err := listenIsLocal(s.cfg.Listen); err == nil && local {
		return host == "127.0.0.1" || host == "localhost" || host == "::1"
	}
	for _, allowed := range s.cfg.AllowedHosts {
		if strings.EqualFold(allowed, host) {
			return true
		}
	}
	return false
}

// healthz is the unauthenticated liveness probe.
func (s *Server) healthz(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		s.logWarn("encode health", err)
	}
}

// sessionInfo returns the principal only; the session token is never echoed.
func (s *Server) sessionInfo(w http.ResponseWriter, _ *http.Request, row *Session) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"principal": row.Principal, "auth": s.cfg.Auth}); err != nil {
		s.logWarn("encode session", err)
	}
}

// methodNotAllowed answers a wrong verb without revealing the valid set.
func methodNotAllowed(w http.ResponseWriter) {
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// originHost extracts the host:port of an Origin header value.
func originHost(origin string) string {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}
