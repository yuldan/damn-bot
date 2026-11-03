//go:build !minimal

package provider

import (
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/damnbot/damnbot/internal/media/preflight"
	"github.com/damnbot/damnbot/internal/netx/retry"
)

// MaxImages bounds one message's attachments.
const MaxImages = 16

// Vision failures.
var (
	ErrNoVision = errors.New("provider: no vision-capable route")
	ErrImage    = errors.New("provider: invalid image attachment")
)

// PrepareImages resizes every attachment to the provider preset before it can
// enter a prompt. MIME and geometry are the honest post-preflight values; an
// unsupported or malformed image fails closed (no request is sent).
func PrepareImages(images []Image) ([]Image, error) {
	if len(images) > MaxImages {
		return nil, fmt.Errorf("%w: %d images > %d", ErrImage, len(images), MaxImages)
	}
	out := make([]Image, len(images))
	lim := preflight.Default()
	for i, img := range images {
		if len(img.Data) == 0 {
			return nil, fmt.Errorf("%w: %s has no data", ErrImage, img.Name)
		}
		res, err := preflight.Preflight(img.Data, lim)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrImage, img.Name, err)
		}
		out[i] = Image{
			Name: img.Name, MIME: res.MIME, Data: res.Data,
			Width: res.Width, Height: res.Height,
			OriginalWidth: res.OriginalWidth, OriginalHeight: res.OriginalHeight,
			OriginalBytes: len(img.Data), Resized: res.Resized, Untrusted: img.Untrusted,
		}
	}
	return out, nil
}

func (o *OpenAI) visionRequest(r *Request, chain []string) (*Request, bool, error) {
	if !requestImages(r) {
		return r, false, nil
	}
	if !o.hasVision(chain) {
		return nil, true, &Error{Class: retry.BadRequest, Err: ErrNoVision}
	}
	prepared, err := prepareRequest(r)
	if err != nil {
		return nil, true, &Error{Class: retry.BadRequest, Err: err}
	}
	return prepared, true, nil
}

func requestImages(r *Request) bool {
	for i := range r.Messages {
		if len(r.Messages[i].Images) > 0 {
			return true
		}
	}
	return false
}

func prepareRequest(r *Request) (*Request, error) {
	out := *r
	out.Messages = make([]Message, len(r.Messages))
	copy(out.Messages, r.Messages)
	for i := range out.Messages {
		if len(out.Messages[i].Images) == 0 {
			continue
		}
		prepared, err := PrepareImages(out.Messages[i].Images)
		if err != nil {
			return nil, err
		}
		out.Messages[i].Images = prepared
	}
	return &out, nil
}

func (o *OpenAI) hasVision(chain []string) bool {
	for _, ref := range chain {
		if rt, ok := o.routes[ref]; ok && rt.Vision {
			return true
		}
	}
	return false
}

// imageContent encodes the openai-chat multimodal content parts. The text
// part keeps the cache breakpoint when the route declares one.
func imageContent(rt *Route, m *Message, cache bool, origin string) any {
	text := map[string]any{"type": "text", "text": m.Content}
	if cache && (rt.CacheMarkers || anthropicFamily(rt.Family)) {
		text["cache_control"] = CacheControl(rt, origin)
	}
	parts := []any{text}
	for _, img := range m.Images {
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]string{"url": "data:" + img.MIME + ";base64," + base64.StdEncoding.EncodeToString(img.Data)},
		})
	}
	return parts
}
