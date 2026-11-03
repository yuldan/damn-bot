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
	Kind string  `json:"kind"`
	X    float64 `json:"x,omitempty"`
	Y    float64 `json:"y,omitempty"`
	Text string  `json:"text,omitempty"`
}

// Engine is one page backend: a CDP target, a Camoufox HTTP sidecar, the
// Lightpanda CLI or a cloud HTTP provider (ADR-321). Implementations perform
// raw page operations and return protocol failures; Page owns the origin
// allowlist, the result bounds and approval. Navigate receives the page guard
// so an engine that follows redirects can refuse an off-allowlist target
// instead of letting it load.
type Engine interface {
	Navigate(ctx context.Context, rawURL string, guard func(*url.URL) error) (string, error)
	Eval(ctx context.Context, expression string) (json.RawMessage, error)
	Screenshot(ctx context.Context) ([]byte, error)
	Act(ctx context.Context, a Action) error
	Close() error
}
