package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/i18n"
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
	case strings.HasPrefix(p, "/assets/"):
		s.asset(w, r)
	case p == "/manifest.webmanifest" || p == "/sw.js" || p == "/icon-192.png" || p == "/icon-512.png":
		s.pwaFile(w, r, strings.TrimPrefix(p, "/"))
	case s.authRoute(w, r, p):
	case s.apiRoute(w, r, p):
	case s.settingsRoute(w, r, p):
	case s.pageRoute(w, r, p):
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
	case "/auth/link":
		s.controlLink(w, r)
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

// apiRoute serves the session API and the SSE stream.
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
	case "/api/events":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return true
		}
		s.withSession(w, r, s.events)
	case "/api/ticket":
		if s.mutation(w, r) {
			s.withSession(w, r, s.issueTicket)
		}
	default:
		return false
	}
	return true
}

// pageRoute serves the dashboard, the login exchange page and settings.
func (s *Server) pageRoute(w http.ResponseWriter, r *http.Request, p string) bool {
	switch p {
	case "/auth":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return true
		}
		if s.cfg.Auth == ModeOIDC {
			state := s.nonces.mint(s.now(), s.cfg.NonceTTL)
			http.Redirect(w, r, s.cfg.OIDC.AuthorizeURL(state, newNonce()), http.StatusSeeOther)
			return true
		}
		s.render(w, s.tmpl.auth, s.page(r))
	case "/":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return true
		}
		s.withSession(w, r, s.index)
	case "/settings":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return true
		}
		s.withSession(w, r, s.settings)
	default:
		return false
	}
	return true
}

// page builds the localized template input for r; no token material leaks in.
// The language is read under mu because applySettings updates it at runtime.
func (s *Server) page(r *http.Request) *pageData {
	s.mu.Lock()
	language := s.cfg.Language
	s.mu.Unlock()
	lang := i18n.Resolve(language)
	return &pageData{
		CSRF:     csrfOf(r),
		Lang:     lang,
		Dir:      dirOf(lang),
		Language: lang,
		Locales:  s.cfg.Locales,
		Writable: s.cfg.Settings != nil,
	}
}

// index serves the dashboard shell; the CSRF token reaches the page through a
// meta tag so the cookie can stay HttpOnly.
func (s *Server) index(w http.ResponseWriter, r *http.Request, _ *Session) {
	s.render(w, s.tmpl.index, s.page(r))
}

// settings renders the current UI language, the catalog locales and, when the
// build has a settings writer, the localized preview/apply form.
func (s *Server) settings(w http.ResponseWriter, r *http.Request, _ *Session) {
	s.render(w, s.tmpl.settings, s.page(r))
}

// csrfOf returns the CSRF cookie value for the meta tag.
func csrfOf(r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil {
		return c.Value
	}
	return ""
}

// controlLink mints a one-use URL for `serve open` (GAP-SEC-07). It is
// disabled without a control secret, accepts only a loopback caller, and
// compares the secret in constant time.
func (s *Server) controlLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || s.cfg.ControlSecret == "" {
		http.NotFound(w, r)
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Damnbot-Control")), []byte(s.cfg.ControlSecret)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"url": s.OpenURL(r.URL.Query().Get("remote") == "1")}); err != nil {
		s.logWarn("encode link", err)
	}
}

// settingsRoute serves the preview/apply pair; both are mutating and require
// a session plus the CSRF double-submit token.
func (s *Server) settingsRoute(w http.ResponseWriter, r *http.Request, p string) bool {
	switch p {
	case "/api/settings/preview":
		if s.mutation(w, r) {
			s.withSession(w, r, s.previewSettings)
		}
	case "/api/settings/apply":
		if s.mutation(w, r) {
			s.withSession(w, r, s.applySettings)
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

// ProviderWaitVisible is the REL-07 threshold: a turn silent this long is
// reported to the web frontend as "menunggu provider".
const ProviderWaitVisible = 10 * time.Second

// sessionInfo returns the principal and, after the REL-07 threshold, the
// provider wait in whole seconds. The session token is never echoed.
func (s *Server) sessionInfo(w http.ResponseWriter, _ *http.Request, row *Session) {
	w.Header().Set("Content-Type", "application/json")
	body := map[string]any{"principal": row.Principal, "auth": s.cfg.Auth}
	if s.cfg.ProviderWait != nil {
		if wait := s.cfg.ProviderWait(); wait >= ProviderWaitVisible {
			body["waiting_provider_s"] = int64(wait / time.Second)
		}
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
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
