//go:build !minimal

package gemini_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/gemini"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// reply is one scripted fake generateContent response.
type reply struct {
	status int
	body   string
	raw    string
}

// fake is a loopback generateContent server that replays fixtures and records
// every request body, URI, and header, so a test can prove what was sent.
type fake struct {
	srv    *httptest.Server
	mu     sync.Mutex
	script []reply
	reqs   []string
	uris   []string
	hdrs   []http.Header
}

func newFake(t *testing.T, script ...reply) *fake {
	t.Helper()
	f := &fake{script: script}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) URL() string { return f.srv.URL + "/v1beta" }

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	body, rerr := io.ReadAll(r.Body)
	if rerr != nil {
		http.Error(w, `{"error":{"code":400,"message":"read_body"}}`, http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, string(body))
	f.uris = append(f.uris, r.URL.RequestURI())
	f.hdrs = append(f.hdrs, r.Header.Clone())
	ok := len(f.script) > 0
	var rep reply
	if ok {
		rep, f.script = f.script[0], f.script[1:]
	}
	f.mu.Unlock()
	if !ok {
		http.Error(w, `{"error":{"code":418,"message":"exhausted"}}`, http.StatusTeapot)
		return
	}
	if rep.status != 0 && rep.status != http.StatusOK {
		w.WriteHeader(rep.status)
		if _, werr := io.WriteString(w, rep.body); werr != nil {
			return
		}
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if _, werr := io.WriteString(w, rep.raw); werr != nil {
		return
	}
}

func (f *fake) requests() ([]string, []string, []http.Header) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...), append([]string(nil), f.uris...), append([]http.Header(nil), f.hdrs...)
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name)) // #nosec G304 -- name is a test constant
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return string(b)
}

func route(model, base string) provider.Route {
	return provider.Route{
		Provider: "gemini", Model: model, BaseURL: base, Family: "gemini", APIKey: "k-test",
		Protocols: []registry.Protocol{registry.ProtocolGemini},
	}
}

func newClient(t *testing.T, routes []provider.Route, fallback []string, attempts int) *provider.OpenAI {
	t.Helper()
	n := netx.New(netx.Config{})
	t.Cleanup(n.CloseIdleConnections)
	o, err := provider.NewOpenAI(n.Client("provider", netx.Inference), &provider.Config{
		Routes: routes, Fallback: fallback, Attempts: attempts, UserAgent: "damnbot/test",
		Wires: map[registry.Protocol]provider.Wire{registry.ProtocolGemini: gemini.New()},
		Wait:  func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	return o
}

func ask(text string) *provider.Request {
	return &provider.Request{
		Model:    "gemini/m",
		Messages: []provider.Message{{Role: "user", Content: text}},
	}
}

func decodeBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("request body %s: %v", raw, err)
	}
	return body
}

// TestFixtureText: a text stream becomes content with usage; the request goes
// to the model-scoped :streamGenerateContent endpoint with the API key header
// and no bearer token.
func TestFixtureText(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "text.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
	resp, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Content != "Hello world" || resp.FinishReason != provider.FinishStop || resp.Model != "gemini-test" {
		t.Fatalf("response = %+v", resp)
	}
	if resp.Usage.Input != 12 || resp.Usage.Output != 3 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	reqs, uris, hdrs := f.requests()
	if len(reqs) != 1 || uris[0] != "/v1beta/models/m:streamGenerateContent?alt=sse" {
		t.Fatalf("requests/uris = %d/%v", len(reqs), uris)
	}
	if got := hdrs[0].Get("x-goog-api-key"); got != "k-test" {
		t.Fatalf("x-goog-api-key = %q", got)
	}
	if got := hdrs[0].Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want none", got)
	}
	assertTextRequest(t, reqs[0])
}

// assertTextRequest checks one user text turn in the native shape.
func assertTextRequest(t *testing.T, raw string) {
	t.Helper()
	body := decodeBody(t, raw)
	if body["systemInstruction"] != nil {
		t.Fatalf("unexpected systemInstruction: %v", body["systemInstruction"])
	}
	contents, ok := body["contents"].([]any)
	if !ok || len(contents) != 1 {
		t.Fatalf("contents = %v", body["contents"])
	}
	msg, ok := contents[0].(map[string]any)
	if !ok || msg["role"] != "user" {
		t.Fatalf("contents[0] = %v", contents[0])
	}
	parts, ok := msg["parts"].([]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("parts = %v", msg["parts"])
	}
	first, ok := parts[0].(map[string]any)
	if !ok || first["text"] != "hi" {
		t.Fatalf("text = %v", parts[0])
	}
}

// TestFixtureUsage: cached prompt tokens are carried into budget.Usage.
func TestFixtureUsage(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "usage.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
	resp, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Usage.Input != 100 || resp.Usage.CachedInput != 40 || resp.Usage.Output != 10 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

// TestFixtureBlocked: a prompt blocked before any candidate finishes as
// content_filter instead of an empty response.
func TestFixtureBlocked(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "blocked.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
	resp, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
	if err != nil || resp.FinishReason != provider.FinishContentFilter {
		t.Fatalf("response = %+v, %v", resp, err)
	}
}

// TestFixtureErrorEvent: an in-stream error object is a typed, retryable
// provider error (never a silent empty response).
func TestFixtureErrorEvent(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "error.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 1)
	_, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
	var pe *provider.Error
	if !errors.As(err, &pe) {
		t.Fatalf("error = %v, want *provider.Error", err)
	}
	if pe.Class != retry.RateLimited || !pe.Class.Retryable() || pe.Code != "RESOURCE_EXHAUSTED" {
		t.Fatalf("error = %+v", pe)
	}
	if len(pe.Body) == 0 {
		t.Fatal("error body is empty")
	}
}

// TestRetryAndFallback: retry stays on the route and then the shared engine
// switches to the fallback route; both speak the Gemini adapter.
func TestRetryAndFallback(t *testing.T) {
	t.Parallel()
	t.Run("retry", func(t *testing.T) {
		t.Parallel()
		f := newFake(t, reply{status: http.StatusBadGateway, body: `{"error":{"code":502,"status":"UNAVAILABLE"}}`},
			reply{raw: fixture(t, "text.sse")})
		c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 2)
		resp, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
		if err != nil || resp.Content != "Hello world" || resp.Attempts != 2 {
			t.Fatalf("retry response = %+v, %v", resp, err)
		}
	})
	t.Run("fallback", func(t *testing.T) {
		t.Parallel()
		primary := newFake(t, reply{status: http.StatusInternalServerError, body: `{"error":{"code":500,"status":"INTERNAL"}}`})
		second := newFake(t, reply{raw: fixture(t, "text.sse")})
		routes := []provider.Route{route("m", primary.URL()), route("fb", second.URL())}
		c := newClient(t, routes, []string{"gemini/fb"}, 1)
		resp, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
		if err != nil || resp.Content != "Hello world" || resp.FallbackFrom != "gemini/m" {
			t.Fatalf("fallback response = %+v, %v", resp, err)
		}
	})
}

// TestNoContextProbe (GAP-COST-02): constructing the adapter opens no
// connection, and one turn sends exactly one request carrying user content.
func TestNoContextProbe(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "text.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
	if reqs, _, _ := f.requests(); len(reqs) != 0 {
		t.Fatalf("startup made %d requests, want 0", len(reqs))
	}
	if _, err := c.Do(t.Context(), provider.PurposeTurn, ask("ping"), nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	reqs, _, _ := f.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests after one turn, want 1", len(reqs))
	}
	if !strings.Contains(reqs[0], `"text":"ping"`) {
		t.Fatalf("request without user content: %s", reqs[0])
	}
}

// TestRequestShapes: system instruction, thinking budget, and temperature
// reach the body in the native shape.
func TestRequestShapes(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "text.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
	temp := 0.2
	req := &provider.Request{
		Model: "gemini/m",
		Messages: []provider.Message{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "hi"},
		},
		Temperature:     &temp,
		ReasoningEffort: "high",
	}
	if _, err := c.Do(t.Context(), provider.PurposeTurn, req, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	reqs, _, _ := f.requests()
	body := reqs[0]
	for _, want := range []string{
		`"systemInstruction":{"parts":[{"text":"be brief"}]}`,
		`"thinkingConfig":{"thinkingBudget":4096,"includeThoughts":true}`,
		`"temperature":0.2`,
		`"maxOutputTokens":8192`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %s:\n%s", want, body)
		}
	}
}

// TestExtraBodyRejected: a reserved extra_body key fails the request instead
// of silently overriding a field encode owns.
func TestExtraBodyRejected(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "text.sse")})
	r := route("m", f.URL())
	r.ExtraBody = map[string]any{"contents": []any{}}
	c := newClient(t, []provider.Route{r}, nil, 1)
	_, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Class != retry.BadRequest {
		t.Fatalf("error = %v, want BadRequest", err)
	}
	if reqs, _, _ := f.requests(); len(reqs) != 0 {
		t.Fatalf("%d requests sent, want 0", len(reqs))
	}
}
