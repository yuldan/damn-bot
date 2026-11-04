//go:build minimal

package provider

import "errors"

// ErrNoVision is the image gate symbol; the minimal build has no attachment
// channel that populates Message.Images (preflight is not linked).
var ErrNoVision = errors.New("provider: no vision-capable route")

func (o *OpenAI) visionRequest(r *Request, _ []string) (*Request, bool, error) {
	return r, false, nil
}

func imageContent(_ *Route, m *Message, _ bool, _ string) any { return m.Content }

func hasMedia(m *Message) bool { return len(m.Images) > 0 || len(m.Videos) > 0 }
