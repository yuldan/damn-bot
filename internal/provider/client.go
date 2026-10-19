package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/netx/retry"
)

// MaxFallback is the longest fallback chain after the primary (SPEC §4.1.1).
const MaxFallback = 2

// ErrConfig rejects an invalid provider configuration.
var ErrConfig = errors.New("provider config")

// Fallback reports a switch to another model (event provider_fallback, P7).
// OnFallback receives it twice per fallback attempt (PROV-M5): before the
// request is sent (Done false: the prompt is about to reach To) and after
// it (Done true, Outcome nil on success), so a failed fallback is visible.
type Fallback struct {
	From, To string
	Err      error // the failure that caused the switch
	Done     bool
	Outcome  error // result of the attempt on To (Done only)
}

// Config configures [NewOpenAI].
type Config struct {
	Routes     []Route
	Fallback   []string // "provider/model" refs, ≤ MaxFallback
	OnFallback func(Fallback)
	OnRetry    func(retry.Attempt) // WARN + event per retry (SPEC §15)
	// Wait is the retry wait (retry.Policy.Wait); nil = real timer. Tests
	// over real loopback sockets inject an instant wait.
	Wait func(ctx context.Context, d time.Duration) error
}

// OpenAI is the openai-chat Client with retry, circuit breaking, and model
// fallback. Safe for concurrent use.
type OpenAI struct {
	c        *netx.Client
	routes   map[string]Route
	fallback []string
	onFB     func(Fallback)
	onRetry  func(retry.Attempt)
	wait     func(ctx context.Context, d time.Duration) error

	mu       sync.Mutex
	breakers map[string]*retry.Breaker // per (provider, credential)
	drop     map[string]map[string]bool
}

// NewOpenAI builds the client over an Inference egress point of netx.
func NewOpenAI(c *netx.Client, cfg Config) (*OpenAI, error) {
	if len(cfg.Fallback) > MaxFallback {
		return nil, fmt.Errorf("%w: %d fallback models > %d", ErrConfig, len(cfg.Fallback), MaxFallback)
	}
	o := &OpenAI{
		c: c, routes: map[string]Route{}, fallback: append([]string(nil), cfg.Fallback...),
		onFB: cfg.OnFallback, onRetry: cfg.OnRetry, wait: cfg.Wait,
		breakers: map[string]*retry.Breaker{}, drop: map[string]map[string]bool{},
	}
	for _, r := range cfg.Routes {
		if r.Provider == "" || r.Model == "" || r.BaseURL == "" {
			return nil, fmt.Errorf("%w: route %q needs provider, model, and base_url", ErrConfig, r.Ref())
		}
		r.Headers = maps.Clone(r.Headers)
		o.routes[r.Ref()] = r
	}
	return o, nil
}

// Do runs r on its model, then on the fallback chain (ADR-102 butir 8):
// every call starts at the primary; fallback only after a retryable class
// exhausted its attempts; missing or too-small fallback routes are skipped.
func (o *OpenAI) Do(ctx context.Context, _ Purpose, r *Request, onChunk func(Chunk)) (Response, error) {
	chain := append([]string{r.Model}, o.fallback...)
	est := rawEstimate(r)
	var lastErr error
	for i, ref := range chain {
		if i > 0 && ref == r.Model {
			continue
		}
		rt, ok := o.routes[ref]
		switch {
		case !ok && i == 0:
			return Response{}, &Error{Class: retry.BadRequest, Err: fmt.Errorf("%w %q", ErrNoRoute, ref)}
		case !ok, i > 0 && rt.ContextWindow > 0 && est > rt.ContextWindow:
			continue // EC-PROV-015, EC-PROV-025
		}
		fb := &Fallback{From: r.Model, To: ref, Err: lastErr}
		o.notify(i, fb)
		resp, err := o.call(ctx, &rt, r, est, onChunk)
		fb.Done, fb.Outcome = true, err
		o.notify(i, fb)
		if err == nil {
			if i > 0 {
				resp.FallbackFrom = r.Model
			}
			return resp, nil
		}
		if !retry.ClassifyErr(err).Retryable() {
			return resp, err
		}
		lastErr = err
	}
	return Response{}, lastErr
}

// notify reports a fallback attempt (i > 0) before and after it (PROV-M5).
func (o *OpenAI) notify(i int, fb *Fallback) {
	if i > 0 && o.onFB != nil {
		o.onFB(*fb)
	}
}

// call runs one route through the retry policy and its breaker.
func (o *OpenAI) call(ctx context.Context, rt *Route, r *Request, est int64, onChunk func(Chunk)) (Response, error) {
	br := o.breaker(rt)
	var resp Response
	attempts := 0
	err := retry.Do(ctx, retry.Policy{Notify: o.onRetry, Wait: o.wait}, func(ctx context.Context, n int) error {
		if err := br.Allow(); err != nil {
			return fmt.Errorf("%s: %w", rt.Ref(), err)
		}
		attempts = n
		var err error
		resp, err = o.send(ctx, rt, r, est, onChunk)
		br.Record(retry.ClassifyErr(err))
		return err
	})
	resp.Attempts, resp.EstInputTokens, resp.CredentialID = attempts, est, rt.CredentialID
	if resp.Model == "" {
		resp.Model = rt.Model
	}
	if err != nil {
		return resp, fmt.Errorf("%s: %w", rt.Ref(), err)
	}
	return resp, nil
}

// send is one attempt; a parameter the route rejects (error.param) is
// dropped for the route and the request re-sent once without it
// (EC-PROV-011/012, ADR-102 butir 7).
func (o *OpenAI) send(ctx context.Context, rt *Route, r *Request, est int64, onChunk func(Chunk)) (Response, error) {
	for {
		drop := o.dropped(rt)
		body, err := encode(rt, r, drop)
		if err != nil {
			return Response{}, &Error{Class: retry.BadRequest, Err: err}
		}
		resp, err := attempt(ctx, o.c, rt, body, est, onChunk)
		p := rejectedParam(err, r, drop)
		if p == "" {
			return resp, err
		}
		o.mu.Lock()
		if o.drop[rt.Ref()] == nil {
			o.drop[rt.Ref()] = map[string]bool{}
		}
		o.drop[rt.Ref()][p] = true
		o.mu.Unlock()
	}
}

func (o *OpenAI) dropped(rt *Route) map[string]bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return maps.Clone(o.drop[rt.Ref()])
}

// rejectedParam returns the optional parameter a 400 names in error.param,
// when r sent it.
func rejectedParam(err error, r *Request, drop map[string]bool) string {
	e, ok := errors.AsType[*Error](err)
	if !ok || e.Class != retry.BadRequest || drop[e.Param] {
		return ""
	}
	switch {
	case e.Param == paramTemperature && r.Temperature != nil, e.Param == paramReasoningEffort && r.ReasoningEffort != "":
		return e.Param
	}
	return ""
}

func (o *OpenAI) breaker(rt *Route) *retry.Breaker {
	key := rt.Provider + "\x00" + rt.CredentialID
	o.mu.Lock()
	defer o.mu.Unlock()
	b := o.breakers[key]
	if b == nil {
		b = new(retry.Breaker)
		o.breakers[key] = b
	}
	return b
}

// rawEstimate is the uncalibrated budget estimate of r (ADR-101): the
// ledger's est_input_tokens and the fallback context-window filter.
func rawEstimate(r *Request) int64 {
	var c budget.Counts
	for i := range r.Messages {
		m := &r.Messages[i]
		c = c.Add(budget.Count(m.Content)).Add(budget.Count(string(m.Reasoning)))
		for _, tc := range m.ToolCalls {
			c = c.Add(budget.Count(tc.Name)).Add(budget.Count(tc.ArgumentsRaw))
		}
	}
	for _, t := range r.Tools {
		c = c.Add(budget.Count(t.Name)).Add(budget.Count(t.Description)).Add(budget.Count(string(t.Parameters)))
	}
	return c.Raw()
}

// attempt sends one request and assembles the stream.
func attempt(ctx context.Context, c *netx.Client, rt *Route, body []byte, est int64, onChunk func(Chunk)) (resp Response, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(rt.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Response{}, &Error{Class: retry.BadRequest, Err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range rt.Headers {
		req.Header.Set(k, v)
	}
	if rt.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+rt.APIKey)
	}
	start := time.Now()
	st, err := c.Stream(ctx, req, netx.TTFB(int(est)))
	if err != nil {
		return Response{}, toError(err)
	}
	defer func() {
		if cerr := st.Close(); cerr != nil && err == nil {
			resp, err = Response{}, toError(cerr)
		}
	}()
	return readStream(st, start, onChunk)
}

// readStream decodes and assembles a 2xx stream.
func readStream(st *netx.Stream, start time.Time, onChunk func(Chunk)) (Response, error) {
	var (
		dec DeltaDecoder
		a   assembler
	)
	for {
		ev, err := st.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Response{}, toError(err)
		}
		if bytes.Equal(ev.Data, []byte("[DONE]")) {
			break
		}
		d, err := dec.Decode(ev.Data)
		if errors.Is(err, ErrTooManyToolDeltas) {
			return Response{}, &Error{Class: retry.Unclassified, Err: fmt.Errorf("%w: %w", ErrResponseTooLarge, err)}
		}
		if err != nil {
			return Response{}, &Error{Class: retry.StreamBroken, Err: err}
		}
		if d.Error != nil {
			return Response{}, &Error{Class: retry.Server, Body: bytes.Clone(d.Error), Err: errors.New("error event in stream")}
		}
		observe(&a, d, start, onChunk)
		if err := a.add(d); err != nil {
			return Response{}, err
		}
	}
	return a.result()
}

// observe records the time to first token and forwards text to onChunk.
func observe(a *assembler, d *Delta, start time.Time, onChunk func(Chunk)) {
	if a.ttft == 0 && (len(d.Content) > 0 || len(d.ToolCalls) > 0 || len(d.Reasoning) > 0) {
		a.ttft = time.Since(start)
	}
	if onChunk != nil && (len(d.Content) > 0 || len(d.Reasoning) > 0) {
		onChunk(Chunk{Content: d.Content, Reasoning: d.Reasoning})
	}
}
