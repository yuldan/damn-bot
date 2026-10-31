package provider

import (
	"fmt"
	"slices"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// Wire is one protocol adapter plugged into [OpenAI]. Retry, circuit, budget
// guard, fallback, and carrier projection/capture stay in the client; a Wire
// only translates a request body and a 2xx event stream over the injected
// [netx.Client] (E-05/`-tags`).
type Wire interface {
	// Encode builds the request body and reports the carriers it embedded.
	Encode(rt *Route, r *Request, drop map[string]bool) ([]byte, []Carrier, []CarrierDrop, error)
	// Path is the endpoint appended to Route.BaseURL.
	Path() string
	// Read decodes a 2xx event stream into a Response.
	Read(st *netx.Stream, start time.Time, onChunk func(Chunk)) (Response, error)
}

// chatWire is the openai-chat adapter (F1) and the default: a route without
// Protocols keeps exactly the previous behaviour.
type chatWire struct{}

func (chatWire) Encode(rt *Route, r *Request, drop map[string]bool) ([]byte, []Carrier, []CarrierDrop, error) {
	return encodeCarriers(rt, r, drop)
}

func (chatWire) Path() string { return "/chat/completions" }

func (chatWire) Read(st *netx.Stream, start time.Time, onChunk func(Chunk)) (Response, error) {
	return readStream(st, start, onChunk)
}

// wireOf returns the wire stored on rt, defaulting to openai-chat for a
// route built by hand (tests, direct callers).
func wireOf(rt *Route) Wire {
	if rt.wire != nil {
		return rt.wire
	}
	return chatWire{}
}

// pickWire selects the wire for rt; a listed protocol without an adapter
// fails at construction instead of reaching the wrong encoder.
func pickWire(rt *Route, wires map[registry.Protocol]Wire) (Wire, error) {
	if len(rt.Protocols) == 0 {
		return chatWire{}, nil
	}
	for _, p := range rt.Protocols {
		if w := wires[p]; w != nil {
			return w, nil
		}
	}
	if slices.Contains(rt.Protocols, registry.ProtocolOpenAIChat) {
		return chatWire{}, nil
	}
	return nil, fmt.Errorf("%w: route %q speaks %v but no adapter is wired", ErrConfig, rt.Ref(), rt.Protocols)
}
