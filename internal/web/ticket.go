package web

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// TicketTTL is the one-use lifetime of a WebSocket/SSE/download ticket
// (GAP-SEC-07). A ticket is not a session token: it authorises exactly one
// stream open and never appears in a page or a stored cookie.
const TicketTTL = 30 * time.Second

// ticket is one outstanding stream authorisation bound to a session hash.
type ticket struct {
	sessionHash string
	expiry      time.Time
}

type ticketSet struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]ticket
}

func (t *ticketSet) init(now func() time.Time) {
	t.now, t.m = now, map[string]ticket{}
}

// mint returns a 128-bit ticket bound to sessionHash.
func (t *ticketSet) mint(sessionHash string, ttl time.Duration) string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.m[token] = ticket{sessionHash: sessionHash, expiry: t.now().Add(ttl)}
	return token
}

// redeem consumes a ticket exactly once. An expired ticket, a replayed one,
// and a ticket presented with a different session cookie all fail alike.
func (t *ticketSet) redeem(token, sessionHash string) bool {
	if token == "" || sessionHash == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	row, ok := t.m[token]
	delete(t.m, token)
	return ok && row.sessionHash == sessionHash && t.now().Before(row.expiry)
}

// issueTicket mints a stream ticket for the authenticated session
// (POST /api/ticket, CSRF + same-origin through mutation).
func (s *Server) issueTicket(w http.ResponseWriter, r *http.Request, row *Session) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token := s.tickets.mint(sessionHash(c.Value), TicketTTL)
	if token == "" {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"ticket": token}); err != nil {
		s.logWarn("encode ticket", err)
	}
}
