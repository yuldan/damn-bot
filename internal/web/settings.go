package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"time"
)

// String renders the patch for diffs and tests.
func (p SettingsPatch) String() string { return "language = \"" + p.Language + "\"" }

// SettingsPatch is one pending `[ui]` change.
type SettingsPatch struct {
	Language string `json:"language"`
}

// SettingsWriter previews and applies a config patch. cmd implements it with
// the atomic config writer; nil disables the settings write path.
type SettingsWriter interface {
	// Preview validates the patch and returns a human-readable diff.
	Preview(ctx context.Context, patch SettingsPatch) (string, error)
	// Apply writes the patch atomically.
	Apply(ctx context.Context, patch SettingsPatch) error
}

// pendingPatch is one confirmed-preview waiting for apply.
type pendingPatch struct {
	patch   SettingsPatch
	diff    string
	expires time.Time
}

const settingsTokenTTL = 2 * time.Minute

// validLanguage accepts auto or a locale of the embedded catalog.
func (s *Server) validLanguage(lang string) bool {
	if lang == "auto" {
		return true
	}
	return slices.Contains(s.cfg.Locales, lang)
}

// previewSettings validates a patch and mints a one-time confirm token.
func (s *Server) previewSettings(w http.ResponseWriter, r *http.Request, _ *Session) {
	if s.cfg.Settings == nil {
		http.Error(w, "settings are read-only in this build", http.StatusNotImplemented)
		return
	}
	var patch SettingsPatch
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&patch); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !s.validLanguage(patch.Language) {
		http.Error(w, "unknown language", http.StatusBadRequest)
		return
	}
	diff, err := s.cfg.Settings.Preview(r.Context(), patch)
	if err != nil {
		s.logWarn("preview settings", err)
		http.Error(w, "invalid settings", http.StatusBadRequest)
		return
	}
	token := newSettingsToken()
	if token == "" {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	s.mu.Lock()
	s.pending[token] = pendingPatch{patch: patch, diff: diff, expires: s.now().Add(settingsTokenTTL)}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"token": token, "diff": diff}); err != nil {
		s.logWarn("encode preview", err)
	}
}

// applySettings writes the confirmed patch exactly once.
func (s *Server) applySettings(w http.ResponseWriter, r *http.Request, _ *Session) {
	if s.cfg.Settings == nil {
		http.Error(w, "settings are read-only in this build", http.StatusNotImplemented)
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	pend, ok := s.pending[body.Token]
	delete(s.pending, body.Token)
	s.mu.Unlock()
	if !ok || s.now().After(pend.expires) {
		http.Error(w, "preview expired", http.StatusConflict)
		return
	}
	if err := s.cfg.Settings.Apply(r.Context(), pend.patch); err != nil {
		s.logWarn("apply settings", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	s.cfg.Language = pend.patch.Language
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// newSettingsToken returns a 128-bit confirm token.
func newSettingsToken() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}
