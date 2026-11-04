//go:build !minimal

package provider

import (
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/damnbot/damnbot/internal/media/preflight"
	"github.com/damnbot/damnbot/internal/media/video"
	"github.com/damnbot/damnbot/internal/netx/retry"
)

// MaxImages bounds one message's attachments.
const MaxImages = 16

// Video bounds of one message (TOOL-081/MEDIA-014). MaxBytes and
// MaxDurationS mirror internal/media/video.
const (
	MaxVideos         = 4
	MaxVideoBytes     = 32 << 20
	MaxVideoDurationS = 120.0
)

// Vision failures.
var (
	ErrNoVision = errors.New("provider: no vision-capable route")
	ErrImage    = errors.New("provider: invalid image attachment")
	ErrVideo    = errors.New("provider: invalid video attachment")
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

// PrepareVideos probes every video and applies the hard byte and duration
// bounds before the request. MIME is the sniffed container type; a malformed
// or oversized video fails closed.
func PrepareVideos(videos []Video) ([]Video, error) {
	if len(videos) > MaxVideos {
		return nil, fmt.Errorf("%w: %d videos > %d", ErrVideo, len(videos), MaxVideos)
	}
	out := make([]Video, len(videos))
	for i, v := range videos {
		if len(v.Data) == 0 {
			return nil, fmt.Errorf("%w: %s has no data", ErrVideo, v.Name)
		}
		if len(v.Data) > MaxVideoBytes {
			return nil, fmt.Errorf("%w: %s has %d bytes > %d", ErrVideo, v.Name, len(v.Data), MaxVideoBytes)
		}
		info, err := video.Probe(v.Data)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrVideo, v.Name, err)
		}
		if info.DurationS > MaxVideoDurationS {
			return nil, fmt.Errorf("%w: %s is %.1fs > %.0fs", ErrVideo, v.Name, info.DurationS, MaxVideoDurationS)
		}
		out[i] = Video{Name: v.Name, MIME: info.MIME, Data: v.Data, DurationS: info.DurationS, Untrusted: v.Untrusted}
	}
	return out, nil
}

func (o *OpenAI) visionRequest(r *Request, chain []string) (*Request, bool, error) {
	if !requestMedia(r) {
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

// hasMedia reports whether m carries any image or video content part.
func hasMedia(m *Message) bool { return len(m.Images) > 0 || len(m.Videos) > 0 }

func requestMedia(r *Request) bool {
	for i := range r.Messages {
		if hasMedia(&r.Messages[i]) {
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
		if len(out.Messages[i].Images) > 0 {
			prepared, err := PrepareImages(out.Messages[i].Images)
			if err != nil {
				return nil, err
			}
			out.Messages[i].Images = prepared
		}
		if len(out.Messages[i].Videos) > 0 {
			prepared, err := PrepareVideos(out.Messages[i].Videos)
			if err != nil {
				return nil, err
			}
			out.Messages[i].Videos = prepared
		}
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
	for _, vid := range m.Videos {
		parts = append(parts, map[string]any{
			"type":      "video_url",
			"video_url": map[string]string{"url": "data:" + vid.MIME + ";base64," + base64.StdEncoding.EncodeToString(vid.Data)},
		})
	}
	return parts
}
