package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Session lifetimes (GAP-SEC-09): 12 h idle and an absolute 7-day cap that is
// never extended.
const (
	IdleTimeout     = 12 * time.Hour
	AbsoluteTimeout = 7 * 24 * time.Hour
	sessionCookie   = "__Host-damnbot"
	csrfCookie      = "damnbot_csrf"
	csrfHeader      = "X-CSRF-Token"
	// endSessionHeader carries the OIDC provider's end_session URL on logout
	// (GAP-SEC-09); the browser navigates there after clearing local state.
	endSessionHeader = "X-End-Session"
)

// Session is one server-side web session. Only the sha256 of the cookie value
// is stored, so a database leak does not expose usable tokens.
type Session struct {
	IDHash         string
	Principal      string
	Created        time.Time
	LastSeen       time.Time
	AbsoluteExpiry time.Time
	RevokedAt      time.Time
}

// SessionStore is the web_sessions table seam (GAP-SEC-09). cmd wires the
// ctxdb implementation; [MemoryStore] serves tests and single-process use.
type SessionStore interface {
	Put(ctx context.Context, s *Session) error
	Get(ctx context.Context, idHash string) (Session, bool, error)
	Touch(ctx context.Context, idHash string, at time.Time) error
	Revoke(ctx context.Context, idHash string) error
}

// BasicVerifier checks the single basic-auth principal (remote mode).
type BasicVerifier interface {
	Verify(user, password string) bool
}

// OIDCProvider is the authorization-code seam (remote mode): it builds the
// redirect URL, exchanges a code for a principal, and returns the provider's
// end_session URL so logout also ends the upstream SSO session (GAP-SEC-09).
type OIDCProvider interface {
	AuthorizeURL(state, nonce string) string
	Exchange(ctx context.Context, code, state string) (principal string, err error)
	EndSessionURL(principal string) string
}

// DrainVerifier checks the non-interactive shared bearer secret of the drain
// control endpoint (UI-015). A verifier is constructed only from a secret of
// at least 256 bits; a weak secret is refused before the listener starts.
type DrainVerifier interface {
	VerifyBearer(token string) bool
}

// nonceSet holds the one-use exchange nonces (GAP-SEC-07).
type nonceSet struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]time.Time
}

func (n *nonceSet) init(now func() time.Time) {
	n.now, n.m = now, map[string]time.Time{}
}

// mint creates a 128-bit nonce valid for ttl.
func (n *nonceSet) mint(_ time.Time, ttl time.Duration) string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand never fails on supported platforms; an empty nonce can
		// never be redeemed, so the listener degrades to refusing entry.
		return ""
	}
	nonce := base64.RawURLEncoding.EncodeToString(raw)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.m[nonce] = n.now().Add(ttl)
	return nonce
}

// redeem consumes a nonce exactly once. It reports false for unknown and
// expired values alike, so a replay is indistinguishable from a typo.
func (n *nonceSet) redeem(nonce string) bool {
	if nonce == "" {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	exp, ok := n.m[nonce]
	delete(n.m, nonce)
	return ok && n.now().Before(exp)
}

// newToken returns a 256-bit session token and its stored hash.
func newToken() (token, hash string) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", ""
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, sessionHash(token)
}

// sessionHash is the web_sessions.id_hash value: sha256 of the cookie token.
func sessionHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// startSession stores a session and returns the cookie pair.
func (s *Server) startSession(ctx context.Context, principal string) (string, error) {
	token, hash := newToken()
	if token == "" {
		return "", errors.New("web: no entropy for session token")
	}
	now := s.now()
	row := Session{IDHash: hash, Principal: principal, Created: now, LastSeen: now, AbsoluteExpiry: now.Add(AbsoluteTimeout)}
	if err := s.cfg.Sessions.Put(ctx, &row); err != nil {
		return "", fmt.Errorf("web: store session: %w", err)
	}
	return token, nil
}

// setSessionCookie sets the HttpOnly `__Host-` cookie plus the CSRF token
// cookie read by the double-submit check.
func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(AbsoluteTimeout.Seconds()),
	})
	csrf := make([]byte, 16)
	if _, err := rand.Read(csrf); err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: base64.RawURLEncoding.EncodeToString(csrf), Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

// exchange redeems a one-use nonce for a session cookie (POST /auth/exchange).
// The nonce arrives in the body, never in the URL or a log line.
func (s *Server) exchange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Nonce string `json:"nonce"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !s.nonces.redeem(body.Nonce) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token, err := s.startSession(r.Context(), "local")
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	setSessionCookie(w, token)
	w.WriteHeader(http.StatusNoContent)
}

// callback completes the OIDC code flow (GET /auth/callback).
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	if s.oauthProvider() == nil || !s.nonces.redeem(r.URL.Query().Get("state")) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	principal, err := s.oauthProvider().Exchange(r.Context(), r.URL.Query().Get("code"), r.URL.Query().Get("state"))
	if err != nil || principal == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token, err := s.startSession(r.Context(), principal)
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	setSessionCookie(w, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// logout revokes the current session, clears both cookies and, in OIDC mode,
// hands the browser the provider's end_session URL (GAP-SEC-09). Revocation is
// server-side, so the old cookie is dead on the very next request.
func (s *Server) logout(w http.ResponseWriter, r *http.Request, row *Session) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if rerr := s.cfg.Sessions.Revoke(r.Context(), sessionHash(c.Value)); rerr != nil {
			s.logWarn("revoke session", rerr)
		}
	}
	if p := s.oauthProvider(); p != nil {
		if end := p.EndSessionURL(row.Principal); end != "" {
			w.Header().Set(endSessionHeader, end)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: "", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

// oauthProvider selects the configured authorization-code seam.
func (s *Server) oauthProvider() OIDCProvider {
	switch s.cfg.Auth {
	case ModeOIDC:
		return s.cfg.OIDC
	case ModeNous:
		return s.cfg.Nous
	default:
		return nil
	}
}

// authenticate resolves the session for r, including the basic-auth mode
// which mints a session from valid credentials.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (Session, bool) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		row, ok, gerr := s.cfg.Sessions.Get(r.Context(), sessionHash(c.Value))
		if gerr == nil && ok {
			if terr := s.cfg.Sessions.Touch(r.Context(), row.IDHash, s.now()); terr != nil {
				s.logWarn("touch session", terr)
			}
			return row, true
		}
	}
	if s.cfg.Auth == ModeDrain && s.cfg.Drain != nil {
		if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && s.cfg.Drain.VerifyBearer(token) {
			return Session{Principal: "drain"}, true
		}
	}
	if s.cfg.Auth == ModeBasic && s.cfg.Basic != nil {
		user, pass, ok := r.BasicAuth()
		if ok && s.cfg.Basic.Verify(user, pass) {
			if token, err := s.startSession(r.Context(), user); err == nil {
				setSessionCookie(w, token)
				return Session{Principal: user}, true
			}
		}
	}
	return Session{}, false
}

// sameOrigin enforces GAP-SEC-08 for state-changing requests: a cross-site
// fetch metadata or a foreign Origin is refused. A non-browser client that
// sends neither header is not a CSRF vector.
func sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "":
	case "same-origin", "none":
	default:
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return originHost(origin) == r.Host
}

// doubleSubmit requires the CSRF cookie to equal the header (GAP-SEC-08).
func doubleSubmit(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return false
	}
	got := r.Header.Get(csrfHeader)
	return subtle.ConstantTimeCompare([]byte(got), []byte(c.Value)) == 1
}

// drainControl is the non-interactive drain endpoint (UI-015). The bearer
// secret is the whole authorization: no cookie, no CSRF, and no token echo.
func (s *Server) drainControl(w http.ResponseWriter, r *http.Request, row *Session) {
	if row.Principal != "drain" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "draining"}); err != nil {
		s.logWarn("encode drain", err)
	}
}
