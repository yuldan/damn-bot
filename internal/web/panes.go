package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// PaneKind is the closed set of dashboard work panes (UI-017). The web UI
// replaces the Electron shell, so the two native panes are terminal and
// preview; both are served by the same session-authenticated API.
type PaneKind string

// Pane kinds. An unknown kind is a typed bad request.
const (
	PaneTerminal PaneKind = "terminal"
	PanePreview  PaneKind = "preview"
)

// Pane is one open pane. ID is server-generated and opaque.
type Pane struct {
	ID      string    `json:"id"`
	Kind    PaneKind  `json:"kind"`
	Title   string    `json:"title"`
	Project string    `json:"project,omitempty"`
	Open    bool      `json:"open"`
	Created time.Time `json:"created"`
}

// PaneOpen is one open request. Target is a workspace-relative preview path or
// a terminal working directory; both are resolved by the backend.
type PaneOpen struct {
	Kind    PaneKind `json:"kind"`
	Target  string   `json:"target,omitempty"`
	Project string   `json:"project,omitempty"`
}

// PaneChunk is one incremental read. Next is the offset for the next call.
type PaneChunk struct {
	Data []byte `json:"data"`
	Next int    `json:"next"`
	Open bool   `json:"open"`
}

// PaneBackend is the process/preview seam. cmd wires the real shell and
// workspace reader; tests and minimal builds inject a fake. A nil backend
// leaves /api/panes unavailable rather than pretending a pane exists.
type PaneBackend interface {
	List(ctx context.Context) ([]Pane, error)
	Open(ctx context.Context, req PaneOpen) (Pane, error)
	Input(ctx context.Context, id string, data []byte) error
	Output(ctx context.Context, id string, offset int) (PaneChunk, error)
	Close(ctx context.Context, id string) error
}

// Project is one named multi-folder workspace (CLI-030) as the dashboard sees
// it. Only names and folders are exposed; no file content is listed here.
type Project struct {
	Name    string   `json:"name"`
	Primary string   `json:"primary,omitempty"`
	Folders []string `json:"folders"`
	Current bool     `json:"current"`
	Board   string   `json:"board,omitempty"`
}

// ProjectSource is the dashboard's read-only view of projects.json. Nil means
// the project surface is unavailable.
type ProjectSource interface {
	Projects(ctx context.Context) ([]Project, error)
}

const maxPaneBody = 1 << 20

// paneRoute serves the pane and project API; it reports whether it answered.
func (s *Server) paneRoute(w http.ResponseWriter, r *http.Request, p string) bool {
	switch p {
	case "/api/panes":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return true
		}
		s.withSession(w, r, s.listPanes)
	case "/api/panes/open":
		if s.mutation(w, r) {
			s.withSession(w, r, s.openPane)
		}
	case "/api/panes/input":
		if s.mutation(w, r) {
			s.withSession(w, r, s.inputPane)
		}
	case "/api/panes/output":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return true
		}
		s.withSession(w, r, s.outputPane)
	case "/api/panes/close":
		if s.mutation(w, r) {
			s.withSession(w, r, s.closePane)
		}
	case "/api/projects":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return true
		}
		s.withSession(w, r, s.listProjects)
	default:
		return false
	}
	return true
}

// writeJSON writes one JSON document without token material.
func (s *Server) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.logWarn("encode json", err)
	}
}

// listPanes lists the open panes of this server.
func (s *Server) listPanes(w http.ResponseWriter, r *http.Request, _ *Session) {
	if s.cfg.Panes == nil {
		http.Error(w, "panes unavailable", http.StatusNotImplemented)
		return
	}
	panes, err := s.cfg.Panes.List(r.Context())
	if err != nil {
		http.Error(w, "panes unavailable", http.StatusServiceUnavailable)
		return
	}
	if panes == nil {
		panes = []Pane{}
	}
	s.writeJSON(w, map[string]any{"panes": panes})
}

// openPane opens one terminal or preview pane.
func (s *Server) openPane(w http.ResponseWriter, r *http.Request, _ *Session) {
	if s.cfg.Panes == nil {
		http.Error(w, "panes unavailable", http.StatusNotImplemented)
		return
	}
	var req PaneOpen
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Kind != PaneTerminal && req.Kind != PanePreview {
		http.Error(w, "unknown pane kind", http.StatusBadRequest)
		return
	}
	pane, err := s.cfg.Panes.Open(r.Context(), req)
	if err != nil {
		http.Error(w, "pane refused", http.StatusBadRequest)
		return
	}
	s.writeJSON(w, pane)
}

// paneID reads the pane id from the query string.
func paneID(r *http.Request) string { return r.URL.Query().Get("id") }

// inputPane writes bytes to a terminal pane.
func (s *Server) inputPane(w http.ResponseWriter, r *http.Request, _ *Session) {
	if s.cfg.Panes == nil {
		http.Error(w, "panes unavailable", http.StatusNotImplemented)
		return
	}
	var req struct {
		ID   string `json:"id"`
		Data string `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.cfg.Panes.Input(r.Context(), req.ID, []byte(req.Data)); err != nil {
		http.Error(w, "pane refused", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// outputPane reads one incremental chunk. A missing id is a bad request and an
// unknown pane is 404, so a stale browser tab degrades visibly.
func (s *Server) outputPane(w http.ResponseWriter, r *http.Request, _ *Session) {
	if s.cfg.Panes == nil {
		http.Error(w, "panes unavailable", http.StatusNotImplemented)
		return
	}
	id := paneID(r)
	if id == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, err := atoiBounded(raw, maxPaneBody)
		if err != nil {
			http.Error(w, "bad offset", http.StatusBadRequest)
			return
		}
		offset = n
	}
	chunk, err := s.cfg.Panes.Output(r.Context(), id, offset)
	if errors.Is(err, ErrPaneMissing) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "pane unavailable", http.StatusServiceUnavailable)
		return
	}
	s.writeJSON(w, chunk)
}

// closePane closes one pane.
func (s *Server) closePane(w http.ResponseWriter, r *http.Request, _ *Session) {
	if s.cfg.Panes == nil {
		http.Error(w, "panes unavailable", http.StatusNotImplemented)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.cfg.Panes.Close(r.Context(), req.ID); err != nil {
		http.Error(w, "pane refused", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listProjects lists the named workspaces available to the dashboard.
func (s *Server) listProjects(w http.ResponseWriter, r *http.Request, _ *Session) {
	if s.cfg.Projects == nil {
		http.Error(w, "projects unavailable", http.StatusNotImplemented)
		return
	}
	projects, err := s.cfg.Projects.Projects(r.Context())
	if err != nil {
		http.Error(w, "projects unavailable", http.StatusServiceUnavailable)
		return
	}
	if projects == nil {
		projects = []Project{}
	}
	s.writeJSON(w, map[string]any{"projects": projects})
}

// atoiBounded parses a non-negative decimal int no larger than max.
func atoiBounded(raw string, max int) (int, error) {
	n := 0
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, errors.New("not a decimal")
		}
		n = n*10 + int(c-'0')
		if n > max {
			return 0, errors.New("out of range")
		}
	}
	if raw == "" {
		return 0, errors.New("empty")
	}
	return n, nil
}
