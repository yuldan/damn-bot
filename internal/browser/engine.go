package browser

import (
	"context"
	"encoding/json"
	"net/url"
)

// Action is one approval-gated page action. Page validates the JSON schema and
// asks for approval before the engine sees it; an engine that cannot perform
// the kind returns errUnsupported.
type Action struct {
	Kind   string  `json:"kind"`
	X      float64 `json:"x,omitempty"`
	Y      float64 `json:"y,omitempty"`
	EndX   float64 `json:"end_x,omitempty"`
	EndY   float64 `json:"end_y,omitempty"`
	DeltaX float64 `json:"delta_x,omitempty"`
	DeltaY float64 `json:"delta_y,omitempty"`
	Text   string  `json:"text,omitempty"`
}

// ReadOp is one page read. Limit bounds any blob reply; Page enforces its own
// caps as well.
type ReadOp struct {
	Kind       string               `json:"kind"`
	Expression string               `json:"expression,omitempty"`
	URL        string               `json:"url,omitempty"`
	Limit      int                  `json:"limit,omitempty"`
	Guard      func(*url.URL) error `json:"-"`
}

// Reply is the result of one read: a JSON value, a blob, or both.
type Reply struct {
	Value json.RawMessage `json:"value,omitempty"`
	Blob  []byte          `json:"-"`
	Name  string          `json:"name,omitempty"`
}

// Engine is one page backend: a CDP target, a Camoufox HTTP sidecar, the
// Lightpanda CLI or a cloud HTTP provider (ADR-321). Page owns the origin
// allowlist, the result bounds and approval; Navigate receives the page guard
// so an engine that follows redirects can refuse an off-allowlist target.
type Engine interface {
	Navigate(ctx context.Context, rawURL string, guard func(*url.URL) error) (string, error)
	Read(ctx context.Context, op ReadOp) (Reply, error)
	Act(ctx context.Context, a *Action) error
	Close() error
}
