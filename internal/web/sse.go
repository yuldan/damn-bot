package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ssePing is the heartbeat interval of /api/events.
const ssePing = 15 * time.Second

// writeFrame writes one SSE event frame and flushes it immediately. Every
// first-token frame of the web UI goes through this function — the stream
// hello today and a chat delta when the dashboard streams one — so
// BenchmarkWebFirstToken measures the production write path rather than a
// copy of it.
func writeFrame(w io.Writer, f http.Flusher, event string, data []byte) error {
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return fmt.Errorf("web: write SSE frame: %w", err)
	}
	f.Flush()
	return nil
}

// events streams the dashboard event channel (SPEC §13.2). The session cookie
// authenticates the request and the 30 s one-use ticket in ?ticket= authorises
// this single stream open (GAP-SEC-07). A ticket is not a session token and is
// dead after the first use, so a copied URL cannot reopen the stream.
func (s *Server) events(w http.ResponseWriter, r *http.Request, row *Session) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || !s.tickets.redeem(r.URL.Query().Get("ticket"), sessionHash(c.Value)) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	hello, err := json.Marshal(map[string]string{"principal": row.Principal})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := writeFrame(w, flusher, "hello", hello); err != nil {
		return
	}
	tick := time.NewTicker(ssePing)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
