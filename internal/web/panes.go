package web

import (
	"context"
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
