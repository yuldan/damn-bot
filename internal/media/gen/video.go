package gen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	videoPollEvery = 100 * time.Millisecond
	videoPollMax   = 60

	// defaultEditSeconds is the conservative price preview for one edit,
	// whose output length is fixed by the source clip.
	defaultEditSeconds = 5
	// minExtendSeconds and maxExtendSeconds bound xAI's extension duration.
	minExtendSeconds = 2
	maxExtendSeconds = 10
	// maxVideoSourceBytes bounds an inline data URI source.
	maxVideoSourceBytes = 8 << 20
	maxVideoSourceURL   = 4096
)

type httpVideo struct {
	name, model string
	price       Price
	doer        Doer
	createURL   string
	statusURL   string
	editURL     string
	extendURL   string
	auth        string
	create      func(*VideoRequest) any
	poll        time.Duration
	maxPolls    int
}

func (v *httpVideo) Name() string { return v.name }

func (v *httpVideo) Price(model string) (Price, bool) { return v.price, model == v.model }

func (v *httpVideo) Generate(ctx context.Context, req *VideoRequest) (VideoResult, error) {
	if req.DurationS <= 0 {
		return VideoResult{}, fmt.Errorf("%w: video duration", ErrInvalidRequest)
	}
	return v.submit(ctx, req, v.createURL, v.create)
}

// Edit applies one prompt to the source video; xAI keeps the input length.
func (v *httpVideo) Edit(ctx context.Context, req *VideoRequest) (VideoResult, error) {
	if err := ValidateVideoSource(req.SourceURL); err != nil {
		return VideoResult{}, err
	}
	if v.editURL == "" {
		return VideoResult{}, fmt.Errorf("%w: video edit endpoint", ErrCapability)
	}
	return v.submit(ctx, req, v.editURL, func(req *VideoRequest) any {
		return map[string]any{
			"model": req.Model, "prompt": req.Prompt,
			"video": map[string]string{"url": req.SourceURL},
		}
	})
}

// Extend appends 2..10 seconds to the source video.
func (v *httpVideo) Extend(ctx context.Context, req *VideoRequest) (VideoResult, error) {
	if err := ValidateVideoSource(req.SourceURL); err != nil {
		return VideoResult{}, err
	}
	if err := ValidateExtendDuration(req.DurationS); err != nil {
		return VideoResult{}, err
	}
	if v.extendURL == "" {
		return VideoResult{}, fmt.Errorf("%w: video extension endpoint", ErrCapability)
	}
	return v.submit(ctx, req, v.extendURL, func(req *VideoRequest) any {
		return map[string]any{
			"model": req.Model, "prompt": req.Prompt,
			"video": map[string]string{"url": req.SourceURL}, "duration": req.DurationS,
		}
	})
}

func (v *httpVideo) submit(ctx context.Context, req *VideoRequest, endpoint string, create func(*VideoRequest) any) (VideoResult, error) {
	data, err := postJSON(ctx, v.doer, endpoint, v.auth, req.IdempotencyKey, create(req))
	if err != nil {
		return VideoResult{}, err
	}
	id, err := videoID(data)
	if err != nil {
		return VideoResult{}, err
	}
	poll, max := v.poll, v.maxPolls
	if poll <= 0 {
		poll = videoPollEvery
	}
	if max <= 0 {
		max = videoPollMax
	}
	for i := range max {
		if i > 0 {
			select {
			case <-ctx.Done():
				return VideoResult{}, fmt.Errorf("%w: %w", ErrUncertain, ctx.Err())
			case <-time.After(poll):
			}
		}
		body, err := getJSON(ctx, v.doer, fmt.Sprintf(v.statusURL, url.PathEscape(id)), v.auth)
		if err != nil {
			return VideoResult{}, err
		}
		res, done, err := videoStatus(body)
		if err != nil {
			return VideoResult{}, err
		}
		if done {
			res.Backend, res.Model = v.name, req.Model
			return res, nil
		}
	}
	return VideoResult{}, fmt.Errorf("%w: video poll exhausted", ErrUncertain)
}

// ValidateExtendDuration bounds the seconds added by one extension.
func ValidateExtendDuration(seconds int) error {
	if seconds < minExtendSeconds || seconds > maxExtendSeconds {
		return fmt.Errorf("%w: extension duration %d s", ErrInvalidRequest, seconds)
	}
	return nil
}

// ValidateVideoSource accepts xAI's public HTTPS .mp4 URL or an inline
// video/mp4 data URI, and rejects anything else before any egress.
func ValidateVideoSource(raw string) error {
	if raw == "" {
		return fmt.Errorf("%w: video edit needs a source video", ErrInvalidRequest)
	}
	if strings.HasPrefix(raw, "data:") {
		const prefix = "data:video/mp4;base64,"
		if !strings.HasPrefix(raw, prefix) {
			return fmt.Errorf("%w: video source data URI", ErrInvalidRequest)
		}
		raw, err := base64.StdEncoding.DecodeString(raw[len(prefix):])
		if err != nil || len(raw) > maxVideoSourceBytes {
			return fmt.Errorf("%w: video source size", ErrInvalidRequest)
		}
		return nil
	}
	if len(raw) > maxVideoSourceURL {
		return fmt.Errorf("%w: video source URL too long", ErrInvalidRequest)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		!strings.HasSuffix(strings.ToLower(u.Path), ".mp4") {
		return fmt.Errorf("%w: video source must be an https .mp4 URL", ErrInvalidRequest)
	}
	return nil
}

func getJSON(ctx context.Context, doer Doer, endpoint, authorization string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build poll request", ErrInvalidRequest)
	}
	req.Header.Set("Authorization", authorization)
	resp, err := doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUncertain, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	closeErr := resp.Body.Close()
	if readErr != nil || closeErr != nil {
		return nil, fmt.Errorf("%w: read poll response", ErrUncertain)
	}
	switch {
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: status %d", ErrUncertain, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("%w: status %d: %s", ErrInvalidRequest, resp.StatusCode, data)
	}
	return data, nil
}

func videoID(data []byte) (string, error) {
	var out struct {
		ID        string `json:"id"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("%w: video create response", ErrInvalidRequest)
	}
	if out.ID != "" {
		return out.ID, nil
	}
	if out.RequestID != "" {
		return out.RequestID, nil
	}
	return "", fmt.Errorf("%w: video create response lacks id", ErrInvalidRequest)
}

func videoStatus(data []byte) (VideoResult, bool, error) {
	var out struct {
		Status   string `json:"status"`
		State    string `json:"state"`
		URL      string `json:"url"`
		VideoURL string `json:"video_url"`
		Output   any    `json:"output"`
		Video    struct {
			URL string `json:"url"`
		} `json:"video"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return VideoResult{}, false, fmt.Errorf("%w: video status response", ErrInvalidRequest)
	}
	state := strings.ToLower(out.Status)
	if state == "" {
		state = strings.ToLower(out.State)
	}
	switch state {
	case "failed", "error", "cancelled", "expired":
		return VideoResult{}, false, fmt.Errorf("%w: video job %s", ErrInvalidRequest, state)
	case "completed", "succeeded", "done", "ready":
	default:
		return VideoResult{}, false, nil
	}
	link := out.URL
	if link == "" {
		link = out.VideoURL
	}
	if link == "" {
		link = out.Video.URL
	}
	if link == "" {
		switch v := out.Output.(type) {
		case string:
			link = v
		case map[string]any:
			if s, ok := v["url"].(string); ok {
				link = s
			}
		}
	}
	if link == "" {
		return VideoResult{}, false, fmt.Errorf("%w: completed video has no url", ErrInvalidRequest)
	}
	return VideoResult{URL: link, MIME: "video/mp4"}, true, nil
}

func newVideo(name, model, endpoint, status, key string, perSecond int64, doer Doer,
	create func(*VideoRequest) any,
) (VideoGen, error) {
	if key == "" || doer == nil {
		return nil, fmt.Errorf("%w: %s video needs a key and egress client", ErrModuleUnavailable, name)
	}
	return &httpVideo{
		name: name, model: model, doer: doer, createURL: endpoint, statusURL: status,
		auth: "Bearer " + key, price: Price{PerSecond: perSecond}, create: create,
		poll: videoPollEvery, maxPolls: videoPollMax,
	}, nil
}

// NewFALVideo is FAL Veo/Kling/Pixverse.
func NewFALVideo(doer Doer, createURL, statusURL, key string) (VideoGen, error) {
	if createURL == "" {
		createURL, statusURL = "https://queue.fal.run/fal-ai/veo3", "https://queue.fal.run/fal-ai/veo3/requests/%s"
	}
	return newVideo("fal", "fal-ai/veo3", createURL, statusURL, key, 400_000, doer,
		func(req *VideoRequest) any {
			return map[string]any{"prompt": req.Prompt, "duration": req.DurationS}
		})
}

// NewXAIVideo is xAI Grok Imagine video.
func NewXAIVideo(doer Doer, createURL, statusURL, key string) (VideoGen, error) {
	if createURL == "" {
		createURL, statusURL = "https://api.x.ai/v1/videos/generations", "https://api.x.ai/v1/videos/%s"
	}
	return newVideo("xai", "grok-imagine-video", createURL, statusURL, key, 300_000, doer,
		func(req *VideoRequest) any {
			return map[string]any{"model": req.Model, "prompt": req.Prompt, "duration": req.DurationS}
		})
}

// NewOpenRouterVideo is OpenRouter's /api/v1/videos polling API.
func NewOpenRouterVideo(doer Doer, createURL, statusURL, key string) (VideoGen, error) {
	if createURL == "" {
		createURL, statusURL = "https://openrouter.ai/api/v1/videos", "https://openrouter.ai/api/v1/videos/%s"
	}
	return newVideo("openrouter", "google/veo-3", createURL, statusURL, key, 350_000, doer,
		func(req *VideoRequest) any {
			return map[string]any{"model": req.Model, "prompt": req.Prompt, "duration": req.DurationS}
		})
}

// NewDeepInfraVideo is DeepInfra text/image-to-video.
func NewDeepInfraVideo(doer Doer, createURL, statusURL, key string) (VideoGen, error) {
	if createURL == "" {
		createURL, statusURL = "https://api.deepinfra.com/v1/videos", "https://api.deepinfra.com/v1/videos/%s"
	}
	return newVideo("deepinfra", "Wan-AI/Wan2.1-T2V-14B", createURL, statusURL, key, 150_000, doer,
		func(req *VideoRequest) any {
			return map[string]any{"model": req.Model, "prompt": req.Prompt, "duration": req.DurationS}
		})
}

// NewXAIVideoEditor is xAI's edit/extend backend. The operation URLs derive
// from the generation URL, so a custom endpoint is honoured without a new
// config key. The key is only ever placed in the Authorization header.
func NewXAIVideoEditor(doer Doer, createURL, statusURL, key string) (VideoEditGen, error) {
	if createURL == "" {
		createURL, statusURL = "https://api.x.ai/v1/videos/generations", "https://api.x.ai/v1/videos/%s"
	}
	if key == "" || doer == nil {
		return nil, fmt.Errorf("%w: xai video edit needs a key and egress client", ErrModuleUnavailable)
	}
	if !strings.Contains(statusURL, "%s") {
		return nil, fmt.Errorf("%w: xai video status URL needs a %%s id", ErrCapability)
	}
	editURL, err := xaiVideoOpURL(createURL, "edits")
	if err != nil {
		return nil, err
	}
	extendURL, err := xaiVideoOpURL(createURL, "extensions")
	if err != nil {
		return nil, err
	}
	return &httpVideo{
		name: "xai", model: "grok-imagine-video", doer: doer,
		createURL: createURL, statusURL: statusURL, editURL: editURL, extendURL: extendURL,
		auth: "Bearer " + key, price: Price{PerSecond: 300_000},
		poll: videoPollEvery, maxPolls: videoPollMax,
	}, nil
}

func xaiVideoOpURL(createURL, op string) (string, error) {
	const suffix = "/generations"
	if !strings.HasSuffix(createURL, suffix) {
		return "", fmt.Errorf("%w: xai video %s needs a create URL ending in /generations", ErrCapability, op)
	}
	return strings.TrimSuffix(createURL, suffix) + "/" + op, nil
}
