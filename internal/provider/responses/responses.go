package responses

import (
	"time"

	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider"
)

// New returns the OpenAI Responses wire adapter. The adapter holds no state
// and never opens a connection: provider.OpenAI drives it over the injected
// netx client (retry, circuit, budget, fallback, and carrier projection are
// shared with openai-chat).
func New() provider.Wire { return wire{} }

type wire struct{}

// Encode builds the /responses request body.
func (wire) Encode(rt *provider.Route, r *provider.Request, drop map[string]bool) ([]byte, []provider.Carrier, []provider.CarrierDrop, error) {
	return encode(rt, r, drop)
}

// Path is the Responses endpoint appended to Route.BaseURL.
func (wire) Path() string { return "/responses" }

// Read decodes a 2xx Responses event stream.
func (wire) Read(st *netx.Stream, start time.Time, onChunk func(provider.Chunk)) (provider.Response, error) {
	return readStream(st, start, onChunk)
}
