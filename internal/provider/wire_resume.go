//go:build !minimal

package provider

import (
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

// Response is the assembled result of a call in the full build.
type Response = responseFull

// ResumeWire continues a dropped stream (EC-PROV-002) without repeating prev.
type ResumeWire interface {
	ReadResume(st *netx.Stream, start time.Time, onChunk func(Chunk), prev *Response) (Response, error)
}

type resumeWire struct {
	Wire
	prev *Response
}

func (r resumeWire) Read(st *netx.Stream, start time.Time, onChunk func(Chunk)) (Response, error) {
	rw, ok := r.Wire.(ResumeWire)
	if !ok {
		resp, err := r.Wire.Read(st, start, onChunk)
		if err != nil {
			return resp, fmt.Errorf("read stream: %w", err)
		}
		return resp, nil
	}
	resp, err := rw.ReadResume(st, start, onChunk, r.prev)
	if err != nil {
		return resp, fmt.Errorf("resume stream: %w", err)
	}
	return resp, nil
}

func wireForAttempt(w Wire, prev *Response) Wire {
	if prev == nil {
		return w
	}
	if _, ok := w.(ResumeWire); !ok {
		return w
	}
	return resumeWire{Wire: w, prev: prev}
}
