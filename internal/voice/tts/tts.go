// Package tts is the text-to-speech half of the voice module (SPEC §9.5): a
// Provider contract and one OpenAI-compatible HTTP backend behind a Doer seam.
// The returned MIME type is sniffed from the bytes, never copied from the
// requested format.
package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// MaxTextBytes is the OpenAI speech request limit.
	MaxTextBytes = 4096
	// MaxAudioBytes is the largest accepted response (25 MiB).
	MaxAudioBytes = 25 << 20
	// DefaultTimeout bounds one HTTP request.
	DefaultTimeout = 2 * time.Minute
)

var (
	// ErrNotConfigured is a provider without an endpoint.
	ErrNotConfigured = errors.New("voice/tts: provider is not configured")
	// ErrEmptyText is empty or whitespace-only input.
	ErrEmptyText = errors.New("voice/tts: empty text")
	// ErrTextTooLong is input over MaxTextBytes.
	ErrTextTooLong = errors.New("voice/tts: text exceeds the limit")
	// ErrStatus is a non-2xx response.
	ErrStatus = errors.New("voice/tts: provider returned an error")
	// ErrBadAudio is a response that is not a named audio container.
	ErrBadAudio = errors.New("voice/tts: response is not audio")
	// ErrTooLarge is a response over MaxAudioBytes.
	ErrTooLarge = errors.New("voice/tts: response exceeds the limit")
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

// HTTP is the OpenAI-compatible speech backend.
type HTTP struct {
	endpoint, key, model, voice, format string
	max                                 int64
	doer                                Doer
}

// Config is one HTTP backend; MaxBytes is a test override.
type Config struct {
	Endpoint, Key, Model, Voice, Format string
	MaxBytes                            int64
	Doer                                Doer
}

// NewHTTP fails closed without an http(s) endpoint and a Doer.
func NewHTTP(c *Config) (*HTTP, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: no config", ErrNotConfigured)
	}
	ep := strings.TrimSpace(c.Endpoint)
	if u, err := url.Parse(ep); ep == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%w: endpoint %q must be an http(s) URL", ErrNotConfigured, ep)
	}
	if c.Doer == nil {
		return nil, fmt.Errorf("%w: no HTTP doer", ErrNotConfigured)
	}
	h := &HTTP{endpoint: ep, key: c.Key, model: cmp(c.Model, "tts-1"), voice: cmp(c.Voice, "alloy"), format: cmp(c.Format, "mp3"), max: c.MaxBytes, doer: c.Doer}
	if h.max <= 0 {
		h.max = MaxAudioBytes
	}
	if !formatOK(h.format) {
		return nil, fmt.Errorf("%w: format %q is not supported", ErrBadAudio, h.format)
	}
	return h, nil
}

// Synthesize sends one sentence and returns at most max bytes of audio.
func (h *HTTP) Synthesize(ctx context.Context, text string, o Options) (Audio, error) {
	body := strings.TrimSpace(text)
	if body == "" {
		return Audio{}, ErrEmptyText
	}
	if len(body) > MaxTextBytes {
		return Audio{}, fmt.Errorf("%w: %d > %d bytes", ErrTextTooLong, len(body), MaxTextBytes)
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	format := cmp(o.Format, h.format)
	if !formatOK(format) {
		return Audio{}, fmt.Errorf("%w: format %q is not supported", ErrBadAudio, format)
	}
	payload, err := json.Marshal(map[string]string{"model": h.model, "input": body, "voice": h.voice, "response_format": format})
	if err != nil {
		return Audio{}, fmt.Errorf("voice/tts: encode: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, h.endpoint, bytes.NewReader(payload))
	if err != nil {
		return Audio{}, fmt.Errorf("voice/tts: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if h.key != "" {
		req.Header.Set("Authorization", "Bearer "+h.key)
	}
	resp, err := h.doer.Do(req)
	if err != nil {
		if cctx.Err() != nil {
			return Audio{}, fmt.Errorf("voice/tts: request: %w", cctx.Err())
		}
		return Audio{}, fmt.Errorf("voice/tts: request: %w", err)
	}
	raw, rerr := io.ReadAll(io.LimitReader(resp.Body, h.max+1))
	if jerr := errors.Join(rerr, resp.Body.Close()); jerr != nil {
		return Audio{}, fmt.Errorf("voice/tts: body: %w", jerr)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Audio{}, fmt.Errorf("%w: %s: %s", ErrStatus, resp.Status, clip(string(raw)))
	}
	if int64(len(raw)) > h.max {
		return Audio{}, fmt.Errorf("%w: over %d bytes", ErrTooLarge, h.max)
	}
	a, err := classify(raw, resp.Header.Get("Content-Type"))
	if err != nil {
		return Audio{}, err
	}
	a.Backend = "openai"
	return a, nil
}

// classify trusts magic bytes over the Content-Type header.
func classify(raw []byte, contentType string) (Audio, error) {
	if len(raw) == 0 {
		return Audio{}, fmt.Errorf("%w: empty response", ErrBadAudio)
	}
	if mt, ok := sniff(raw); ok {
		ext, _ := extFor(mt)
		return Audio{MIME: mt, Ext: ext, Bytes: raw}, nil
	}
	if mt, _, err := mime.ParseMediaType(contentType); err == nil {
		if ext, ok := extFor(mt); ok {
			return Audio{MIME: mt, Ext: ext, Bytes: raw}, nil
		}
	}
	return Audio{}, fmt.Errorf("%w: content type %q", ErrBadAudio, contentType)
}

// formatOK reports whether a requested response format is supported.
func formatOK(format string) bool {
	switch format {
	case "mp3", "wav", "opus", "aac", "flac", "pcm":
		return true
	}
	return false
}

// extFor returns the extension of a MIME type this build accepts.
func extFor(mt string) (string, bool) {
	switch mt {
	case "audio/mpeg":
		return ".mp3", true
	case "audio/wav", "audio/x-wav", "audio/wave":
		return ".wav", true
	case "audio/ogg":
		return ".ogg", true
	case "audio/aac":
		return ".aac", true
	case "audio/flac", "audio/x-flac":
		return ".flac", true
	case "audio/pcm", "audio/L16":
		return ".pcm", true
	}
	return "", false
}

// sniff recognises the containers SPEC §9.5 names.
func sniff(raw []byte) (string, bool) {
	switch {
	case len(raw) >= 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WAVE":
		return "audio/wav", true
	case bytes.HasPrefix(raw, []byte("OggS")):
		return "audio/ogg", true
	case bytes.HasPrefix(raw, []byte("fLaC")):
		return "audio/flac", true
	case bytes.HasPrefix(raw, []byte("ID3")):
		return "audio/mpeg", true
	case len(raw) >= 2 && raw[0] == 0xFF && raw[1]&0xE0 == 0xE0:
		return "audio/mpeg", true
	case len(raw) >= 2 && raw[0] == 0xFF && raw[1]&0xF0 == 0xF0:
		return "audio/aac", true
	}
	return "", false
}

// clip keeps a provider error body short enough for a log line.
func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

func cmp(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
