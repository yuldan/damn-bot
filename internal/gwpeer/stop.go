package gwpeer

import (
	"crypto/subtle"
	"net"
	"net/http"
)

// adminStopHeader authenticates the loopback-only admin stop endpoint that
// `damnbot peer stop` calls.
const adminStopHeader = "X-Damnbot-" + "Stop"

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || s.StopToken == "" || !loopbackRequest(r) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	got := r.Header.Get(adminStopHeader)
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.StopToken)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	if s.Stop != nil {
		go s.Stop()
	}
}

// loopbackRequest reports whether the TCP peer is a loopback address.
func loopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
