// Package gen is the media generation core (SPEC §9.5, GAP-COST-11).
package gen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// Typed failures.
var (
	ErrModuleUnavailable = errors.New("media/gen: backend unavailable")
	ErrCapability        = errors.New("media/gen: capability unavailable")
	ErrBudget            = errors.New("media/gen: budget denied")
	ErrApprovalDenied    = errors.New("media/gen: approval denied")
	ErrUncertain         = errors.New("media/gen: vendor outcome uncertain")
	ErrInvalidRequest    = errors.New("media/gen: invalid request")
)

type (
	// ImageRequest is one paid image generation.
	ImageRequest struct {
		SessionID      string
		CallID         string
		IdempotencyKey string
		Model          string
		Prompt         string
		Size           string
	}
	// ImageResult is one generated image as bytes or a vendor URL.
	ImageResult struct {
		Backend string
		Model   string
		Data    []byte
		URL     string
		MIME    string
	}
	// VideoRequest is one paid video generation, edit, extension or analysis.
	// SourceURL carries the existing video for edit/extension only.
	VideoRequest struct {
		SessionID      string
		CallID         string
		IdempotencyKey string
		Model          string
		Prompt         string
		DurationS      int
		SourceURL      string
	}
	// VideoResult is one generated video.
	VideoResult struct {
		Backend string
		Model   string
		URL     string
		MIME    string
	}
	// VideoAnalysis is one analysed video.
	VideoAnalysis struct {
		Backend string
		Model   string
		Text    string
	}
	// Price is one model's price. Micro-USD units match usage_ledger.
	Price struct {
		InputPerMTok  int64
		OutputPerMTok int64
		PerImage      map[string]int64 // size -> micros per image
		PerSecond     int64            // micros per generated second
	}
	// Caps are the SPEC §18.4 non-LLM tool caps in micro-USD. Zero = no cap.
	Caps struct {
		SessionMicros int64
		DailyMicros   int64
		MonthlyMicros int64
	}
)

// ImageGen is one image backend from the closed registry.
type ImageGen interface {
	Name() string
	Price(model string) (Price, bool)
	Generate(ctx context.Context, req *ImageRequest) (ImageResult, error)
}

// VideoGen is one video generation backend.
type VideoGen interface {
	Name() string
	Price(model string) (Price, bool)
	Generate(ctx context.Context, req *VideoRequest) (VideoResult, error)
}

// VideoEditGen is one backend that edits or extends an existing video.
type VideoEditGen interface {
	Name() string
	Price(model string) (Price, bool)
	Edit(ctx context.Context, req *VideoRequest) (VideoResult, error)
	Extend(ctx context.Context, req *VideoRequest) (VideoResult, error)
}

// VideoAnalyzer is one read-only video analysis backend.
type VideoAnalyzer interface {
	Name() string
	Price(model string) (Price, bool)
	Analyze(ctx context.Context, req *VideoRequest) (VideoAnalysis, error)
}

// Meter records a paid tool call as purpose=tool, feature=tool:<backend>.
type Meter func(ctx context.Context, feature string, micros int64) error

// Spend reports known tool spend in [from, to) in micro-USD.
type Spend func(ctx context.Context, from, to time.Time) (int64, error)

// EstimateImage returns the conservative pre-call cost: the size price plus
// prompt input tokens. Output tokens are included in the per-size price.
func (p Price) EstimateImage(size, prompt string) (int64, error) {
	per, ok := p.PerImage[size]
	if !ok || per <= 0 || p.InputPerMTok < 0 {
		return 0, fmt.Errorf("%w: image price for size %q", ErrCapability, size)
	}
	return per + int64(len(prompt)/4+1)*p.InputPerMTok/1_000_000, nil
}

// EstimateVideo returns the per-second price, or ErrCapability.
func (p Price) EstimateVideo(seconds int) (int64, error) {
	if seconds <= 0 || p.PerSecond <= 0 {
		return 0, fmt.Errorf("%w: video price for %d s", ErrCapability, seconds)
	}
	return int64(seconds) * p.PerSecond, nil
}

func imagePrice(input, output int64, sizes map[string]int64) Price {
	return Price{InputPerMTok: input, OutputPerMTok: output, PerImage: sizes}
}

func splitSize(size string) (int, int, error) {
	w, h, ok := strings.Cut(size, "x")
	wi, errW := strconv.Atoi(w)
	hi, errH := strconv.Atoi(h)
	if !ok || errW != nil || errH != nil || wi <= 0 || hi <= 0 {
		return 0, 0, fmt.Errorf("%w: size %q", ErrInvalidRequest, size)
	}
	return wi, hi, nil
}

type job struct {
	running bool
	result  any
	err     error
}

// Service owns idempotent paid jobs, the budget guard and metering.
type Service struct {
	Guard    *Guard
	Approval host.Approvals
	Meter    Meter

	mu   sync.Mutex
	jobs map[string]job
}

// Image generates one image; one key never dispatches a second vendor job.
func (s *Service) Image(ctx context.Context, backend ImageGen, req *ImageRequest) (ImageResult, error) {
	if backend == nil {
		return ImageResult{}, fmt.Errorf("%w: nil image backend", ErrInvalidRequest)
	}
	key, err := idemKey(req.SessionID, req.CallID, req.IdempotencyKey)
	if err != nil {
		return ImageResult{}, err
	}
	price, ok := backend.Price(req.Model)
	if !ok {
		return ImageResult{}, fmt.Errorf("%w: price for %s/%s", ErrCapability, backend.Name(), req.Model)
	}
	estimate, err := price.EstimateImage(req.Size, req.Prompt)
	if err != nil {
		return ImageResult{}, err
	}
	req.IdempotencyKey = key
	v, err := s.run(ctx, key, estimate, nil, func() (any, error) {
		return backend.Generate(ctx, req)
	}, "tool:"+backend.Name())
	if err != nil {
		return ImageResult{}, err
	}
	res, ok := v.(ImageResult)
	if !ok {
		return ImageResult{}, fmt.Errorf("%w: image result", ErrInvalidRequest)
	}
	return res, nil
}

// Video generates one video. It is always approval + price preview, non
// idempotent, and budget-checked before dispatch.
func (s *Service) Video(ctx context.Context, backend VideoGen, req *VideoRequest) (VideoResult, error) {
	if backend == nil {
		return VideoResult{}, fmt.Errorf("%w: nil video backend", ErrInvalidRequest)
	}
	key, err := idemKey(req.SessionID, req.CallID, req.IdempotencyKey)
	if err != nil {
		return VideoResult{}, err
	}
	price, ok := backend.Price(req.Model)
	if !ok {
		return VideoResult{}, fmt.Errorf("%w: price for %s/%s", ErrCapability, backend.Name(), req.Model)
	}
	estimate, err := price.EstimateVideo(req.DurationS)
	if err != nil {
		return VideoResult{}, err
	}
	req.IdempotencyKey = key
	before := func(ctx context.Context, micros int64) error {
		return s.approve(ctx, req, "video_generate", backend.Name(), micros)
	}
	v, err := s.run(ctx, key, estimate, before, func() (any, error) {
		return backend.Generate(ctx, req)
	}, "tool:"+backend.Name())
	if err != nil {
		return VideoResult{}, err
	}
	res, ok := v.(VideoResult)
	if !ok {
		return VideoResult{}, fmt.Errorf("%w: video result", ErrInvalidRequest)
	}
	return res, nil
}

// EditVideo edits (extend=false) or extends (extend=true) one video. It is
// approval + price-preview gated, idempotent per key, and never retries an
// uncertain vendor outcome.
func (s *Service) EditVideo(ctx context.Context, backend VideoEditGen, req *VideoRequest, extend bool) (VideoResult, error) {
	if backend == nil {
		return VideoResult{}, fmt.Errorf("%w: nil video editor", ErrInvalidRequest)
	}
	if req.DurationS <= 0 {
		if extend {
			return VideoResult{}, fmt.Errorf("%w: extension duration", ErrInvalidRequest)
		}
		req.DurationS = defaultEditSeconds
	}
	if req.SourceURL == "" || req.Prompt == "" {
		return VideoResult{}, fmt.Errorf("%w: video edit needs a source and prompt", ErrInvalidRequest)
	}
	if err := ValidateVideoSource(req.SourceURL); err != nil {
		return VideoResult{}, err
	}
	if extend {
		if err := ValidateExtendDuration(req.DurationS); err != nil {
			return VideoResult{}, err
		}
	}
	key, err := idemKey(req.SessionID, req.CallID, req.IdempotencyKey)
	if err != nil {
		return VideoResult{}, err
	}
	price, ok := backend.Price(req.Model)
	if !ok {
		return VideoResult{}, fmt.Errorf("%w: price for %s/%s", ErrCapability, backend.Name(), req.Model)
	}
	estimate, err := price.EstimateVideo(req.DurationS)
	if err != nil {
		return VideoResult{}, err
	}
	req.IdempotencyKey = key
	tool := "xai_video_edit"
	call := func() (any, error) { return backend.Edit(ctx, req) }
	if extend {
		tool = "xai_video_extend"
		call = func() (any, error) { return backend.Extend(ctx, req) }
	}
	before := func(ctx context.Context, micros int64) error {
		return s.approve(ctx, req, tool, backend.Name(), micros)
	}
	v, err := s.run(ctx, key, estimate, before, call, "tool:"+backend.Name())
	if err != nil {
		return VideoResult{}, err
	}
	res, ok := v.(VideoResult)
	if !ok {
		return VideoResult{}, fmt.Errorf("%w: video edit result", ErrInvalidRequest)
	}
	return res, nil
}

// AnalyzeVideo analyses a video read-only (no approval).
func (s *Service) AnalyzeVideo(ctx context.Context, backend VideoAnalyzer, req *VideoRequest) (VideoAnalysis, error) {
	if backend == nil {
		return VideoAnalysis{}, fmt.Errorf("%w: nil analyzer", ErrInvalidRequest)
	}
	key, err := idemKey(req.SessionID, req.CallID, req.IdempotencyKey)
	if err != nil {
		return VideoAnalysis{}, err
	}
	price, ok := backend.Price(req.Model)
	if !ok {
		return VideoAnalysis{}, fmt.Errorf("%w: price for %s/%s", ErrCapability, backend.Name(), req.Model)
	}
	tokens := int64(len(req.Prompt)/4 + 1)
	estimate := tokens * (price.InputPerMTok + price.OutputPerMTok) / 1_000_000
	if estimate <= 0 {
		return VideoAnalysis{}, fmt.Errorf("%w: text price", ErrCapability)
	}
	req.IdempotencyKey = key
	v, err := s.run(ctx, key, estimate, nil, func() (any, error) {
		return backend.Analyze(ctx, req)
	}, "tool:"+backend.Name())
	if err != nil {
		return VideoAnalysis{}, err
	}
	res, ok := v.(VideoAnalysis)
	if !ok {
		return VideoAnalysis{}, fmt.Errorf("%w: analysis result", ErrInvalidRequest)
	}
	return res, nil
}

func (s *Service) approve(ctx context.Context, req *VideoRequest, tool, backend string, micros int64) error {
	a := s.Approval
	if a == nil {
		a = host.DenyApprovals{}
	}
	ar := &host.ApprovalRequest{
		Tool: tool, CallID: req.CallID, Effect: host.EffectNonIdempotent,
		Preview: fmt.Sprintf("%s %s: %d s, estimated $%.4f", backend, req.Model, req.DurationS, float64(micros)/1e6),
	}
	answer, err := a.Approve(ctx, ar)
	if err != nil {
		return fmt.Errorf("media/gen: approval: %w", err)
	}
	if !answer.Allowed() {
		return ErrApprovalDenied
	}
	return nil
}

func (s *Service) run(ctx context.Context, key string, estimate int64, before func(context.Context, int64) error, call func() (any, error), feature string) (any, error) {
	s.mu.Lock()
	if s.jobs == nil {
		s.jobs = map[string]job{}
	}
	if j, ok := s.jobs[key]; ok {
		s.mu.Unlock()
		if j.running || errors.Is(j.err, ErrUncertain) {
			return nil, ErrUncertain
		}
		return j.result, j.err
	}
	s.jobs[key] = job{running: true}
	s.mu.Unlock()

	if err := s.Guard.Check(ctx, estimate); err != nil {
		s.forget(key)
		return nil, err
	}
	if before != nil {
		if err := before(ctx, estimate); err != nil {
			s.forget(key)
			return nil, err
		}
	}
	v, err := call()
	if err != nil {
		if errors.Is(err, ErrUncertain) {
			s.finish(key, nil, ErrUncertain)
			return nil, fmt.Errorf("%w: %w", ErrUncertain, err)
		}
		s.finish(key, nil, err)
		return nil, err
	}
	s.Guard.Record(estimate)
	if s.Meter != nil {
		if err := s.Meter(ctx, feature, estimate); err != nil {
			s.finish(key, v, fmt.Errorf("media/gen: meter: %w", err))
			return v, fmt.Errorf("media/gen: meter: %w", err)
		}
	}
	s.finish(key, v, nil)
	return v, nil
}

func (s *Service) forget(key string) {
	s.mu.Lock()
	delete(s.jobs, key)
	s.mu.Unlock()
}

func (s *Service) finish(key string, result any, err error) {
	s.mu.Lock()
	s.jobs[key] = job{result: result, err: err}
	s.mu.Unlock()
}

func idemKey(session, call, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if session == "" && call == "" {
		return "", fmt.Errorf("%w: idempotency key or session+call id required", ErrInvalidRequest)
	}
	sum := sha256.Sum256([]byte(session + "\x00" + call))
	return hex.EncodeToString(sum[:]), nil
}

// Guard enforces non-LLM tool caps before a vendor call (GAP-COST-11).
type Guard struct {
	Caps  Caps
	Spend Spend
	Now   func() time.Time
	Warn  func(period string, used, limit int64)

	mu      sync.Mutex
	session int64
}

// Check rejects a call whose projection reaches any cap. estimate < 0 is an
// unpriced call and fails closed as ErrCapability, never free.
func (g *Guard) Check(ctx context.Context, estimate int64) error {
	if estimate < 0 {
		return fmt.Errorf("%w: negative estimate", ErrCapability)
	}
	if g == nil {
		return nil
	}
	now := time.Now
	if g.Now != nil {
		now = g.Now
	}
	t := now()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	month := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	if err := g.checkPeriod(ctx, "daily", g.Caps.DailyMicros, estimate, day, day.AddDate(0, 0, 1)); err != nil {
		return err
	}
	if err := g.checkPeriod(ctx, "monthly", g.Caps.MonthlyMicros, estimate, month, month.AddDate(0, 1, 0)); err != nil {
		return err
	}
	g.mu.Lock()
	used := g.session
	g.mu.Unlock()
	projected := used + estimate
	if g.Caps.SessionMicros > 0 && projected >= g.Caps.SessionMicros*4/5 && g.Warn != nil {
		g.Warn("session", projected, g.Caps.SessionMicros)
	}
	if g.Caps.SessionMicros > 0 && projected >= g.Caps.SessionMicros {
		return &BudgetError{
			Period: "session", ProjectedMicros: projected,
			LimitMicros: g.Caps.SessionMicros, Raise: raiseCommand("session"),
		}
	}
	return nil
}

// BudgetError is a paid-tool denial (GAP-COST-11, stop `budget_tools`). It
// carries the numbers, the period and the command that raises the cap so the
// tool-result payload never needs a second lookup. Unwrap keeps errors.Is.
type BudgetError struct {
	Period          string
	ProjectedMicros int64
	LimitMicros     int64
	Reset           time.Time
	Raise           string
}

// Error implements error.
func (e *BudgetError) Error() string {
	reset := ""
	if !e.Reset.IsZero() {
		reset = ", reset " + e.Reset.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("budget_tools: tools_%s cap reached (%d of %d µUSD%s); raise it with %#q",
		e.Period, e.ProjectedMicros, e.LimitMicros, reset, e.Raise)
}

// Unwrap maps the typed denial to ErrBudget.
func (e *BudgetError) Unwrap() error { return ErrBudget }

func raiseCommand(period string) string {
	return "damnbot usage budget set tools_" + period + "_usd 15"
}

func (g *Guard) checkPeriod(ctx context.Context, name string, limit, estimate int64, from, to time.Time) error {
	if limit <= 0 {
		return nil
	}
	if g.Spend == nil {
		return fmt.Errorf("%w: %s totals unavailable", ErrBudget, name)
	}
	used, err := g.Spend(ctx, from, to)
	if err != nil {
		return fmt.Errorf("%w: %s totals: %w", ErrBudget, name, err)
	}
	if used < 0 {
		return fmt.Errorf("%w: %s totals invalid", ErrBudget, name)
	}
	projected := used + estimate
	if projected < used {
		return fmt.Errorf("%w: %s totals overflow", ErrBudget, name)
	}
	if projected >= limit*4/5 && g.Warn != nil {
		g.Warn(name, projected, limit)
	}
	if projected >= limit {
		return &BudgetError{
			Period: name, ProjectedMicros: projected, LimitMicros: limit,
			Reset: to, Raise: raiseCommand(name),
		}
	}
	return nil
}

// Record adds one successful paid call to session spend.
func (g *Guard) Record(micros int64) {
	if g == nil || micros <= 0 {
		return
	}
	g.mu.Lock()
	g.session += micros
	g.mu.Unlock()
}

const (
	openAIEndpoint = "https://api.openai.com/v1/images/generations"
	falEndpoint    = "https://fal.run/fal-ai/flux/dev"
)

// Doer is the egress seam; the composition root passes a netx.Client. This
// package never builds an http.Client of its own.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

func postJSON(ctx context.Context, doer Doer, endpoint, authorization, idem string, payload any) ([]byte, error) {
	if doer == nil {
		return nil, fmt.Errorf("%w: nil egress client", ErrModuleUnavailable)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: encode request", ErrInvalidRequest)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build request", ErrInvalidRequest)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authorization)
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	resp, err := doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUncertain, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	closeErr := resp.Body.Close()
	if readErr != nil || closeErr != nil {
		return nil, fmt.Errorf("%w: read response", ErrUncertain)
	}
	switch {
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: status %d", ErrUncertain, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("%w: status %d: %s", ErrInvalidRequest, resp.StatusCode, data)
	}
	return data, nil
}

type httpImage struct {
	name, model string
	price       Price
	doer        Doer
	endpoint    string
	auth        string
	encode      func(*ImageRequest) any
	decode      func([]byte, *ImageRequest) (ImageResult, error)
}

func (h *httpImage) Name() string { return h.name }

func (h *httpImage) Price(model string) (Price, bool) { return h.price, model == h.model }

func (h *httpImage) Generate(ctx context.Context, req *ImageRequest) (ImageResult, error) {
	if _, _, err := splitSize(req.Size); err != nil {
		return ImageResult{}, err
	}
	data, err := postJSON(ctx, h.doer, h.endpoint, h.auth, req.IdempotencyKey, h.encode(req))
	if err != nil {
		return ImageResult{}, err
	}
	res, err := h.decode(data, req)
	if err != nil {
		return ImageResult{}, err
	}
	res.Backend, res.Model = h.name, req.Model
	return res, nil
}

// NewOpenAIImages fails closed without an API key.
func NewOpenAIImages(doer Doer, endpoint, key string) (ImageGen, error) {
	if key == "" || doer == nil {
		return nil, fmt.Errorf("%w: openai images needs a key and egress client", ErrModuleUnavailable)
	}
	if endpoint == "" {
		endpoint = openAIEndpoint
	}
	h := &httpImage{
		name: "openai", model: "gpt-image-1", doer: doer, endpoint: endpoint, auth: "Bearer " + key,
		price: imagePrice(5_000_000, 40_000_000, map[string]int64{
			"1024x1024": 40_000, "1024x1536": 60_000, "1536x1024": 60_000,
		}),
		encode: func(req *ImageRequest) any {
			return map[string]any{"model": req.Model, "prompt": req.Prompt, "size": req.Size, "n": 1}
		},
		decode: decodeOpenAI,
	}
	return h, nil
}

func decodeOpenAI(data []byte, req *ImageRequest) (ImageResult, error) {
	var out struct {
		Data []struct {
			B64 string `json:"b64_json"`
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Data) == 0 {
		return ImageResult{}, fmt.Errorf("%w: openai response", ErrInvalidRequest)
	}
	res := ImageResult{URL: out.Data[0].URL}
	if out.Data[0].B64 != "" {
		raw, err := base64.StdEncoding.DecodeString(out.Data[0].B64)
		if err != nil {
			return ImageResult{}, fmt.Errorf("%w: openai image bytes", ErrInvalidRequest)
		}
		res.Data, res.MIME = raw, http.DetectContentType(raw)
	}
	return res, nil
}

// NewFALImages fails closed without an API key.
func NewFALImages(doer Doer, endpoint, key string) (ImageGen, error) {
	if key == "" || doer == nil {
		return nil, fmt.Errorf("%w: fal images needs a key and egress client", ErrModuleUnavailable)
	}
	if endpoint == "" {
		endpoint = falEndpoint
	}
	h := &httpImage{
		name: "fal", model: "fal-ai/flux/dev", doer: doer, endpoint: endpoint, auth: "Key " + key,
		price: imagePrice(0, 0, map[string]int64{
			"1024x1024": 25_000, "1024x1536": 37_500, "1536x1024": 37_500,
		}),
		encode: func(req *ImageRequest) any {
			var w, h int
			if wi, hi, err := splitSize(req.Size); err == nil {
				w, h = wi, hi
			}
			return map[string]any{
				"prompt": req.Prompt, "num_images": 1,
				"image_size": map[string]int{"width": w, "height": h},
			}
		},
		decode: decodeFAL,
	}
	return h, nil
}

func decodeFAL(data []byte, req *ImageRequest) (ImageResult, error) {
	var out struct {
		Images []struct {
			URL  string `json:"url"`
			MIME string `json:"mime_type"`
		} `json:"images"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Images) == 0 {
		return ImageResult{}, fmt.Errorf("%w: fal response", ErrInvalidRequest)
	}
	return ImageResult{URL: out.Images[0].URL, MIME: out.Images[0].MIME}, nil
}
