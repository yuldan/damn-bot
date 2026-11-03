// Package tts is the text-to-speech half of the voice module (SPEC §9.5): the
// Provider contract and the honest audio shape shared by the closed backend
// catalog. The HTTP transport lands with the streaming slice.
package tts

import (
	"context"
	"net/http"
	"time"
)

// Audio is one synthesized clip with an honest MIME type and extension.
type Audio struct {
	MIME    string
	Ext     string
	Bytes   []byte
	Backend string
}

// Options are the per-call knobs of a synthesis.
type Options struct {
	Format  string
	Timeout time.Duration
}

// Provider is one text-to-speech backend from the closed catalog.
type Provider interface {
	Synthesize(ctx context.Context, text string, opts Options) (Audio, error)
}

// Doer is the egress seam; the composition root passes a netx.Client.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}
