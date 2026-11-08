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

// setSecurityHeaders stamps every response (GAP-SEC-06). The CSP is the same
// for HTML, assets and JSON so an error page can never weaken it.
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

// ServeHTTP is the router: security headers, the Host allowlist, then the
// public endpoints this slice owns (/healthz and the CSP report sink). Every
// other path is 404 until the auth slice lands.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w, newNonce())
	if !s.hostAllowed(r.Host) {
		http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
		return
	}
	switch r.URL.Path {
	case "/healthz":
		s.healthz(w)
	case "/csp-report":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

// hostAllowed is the anti-DNS-rebinding check (GAP-SEC-08): a loopback bind
// accepts only the three loopback names, a remote bind only the allowlist.
func (s *Server) hostAllowed(hostport string) bool {
	if hostport == "" {
		return true // HTTP/1.0 or a unix peer without a Host header
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

// healthz is the unauthenticated liveness probe; it names no subsystem state.
func (s *Server) healthz(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		s.logWarn("encode health", err)
	}
}
