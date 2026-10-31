package responses_test

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
	"github.com/damnbot/damnbot/internal/provider/registry"
	"github.com/damnbot/damnbot/internal/provider/responses"
)

// reply is one scripted fake Responses response.
type reply struct {
	status int
	body   string
	raw    string
}

// fake is a loopback /v1/responses server that replays fixtures and records
// every request body, so a test can prove the adapter sends nothing extra.
type fake struct {
	srv    *httptest.Server
	mu     sync.Mutex
	script []reply
	reqs   []string
	paths  []string
}

func newFake(t *testing.T, script ...reply) *fake {
	t.Helper()
	f := &fake{script: script}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) URL() string { return f.srv.URL + "/v1" }

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	body, rerr := io.ReadAll(r.Body)
	if rerr != nil {
		http.Error(w, `{"error":{"code":"read_body"}}`, http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, string(body))
	f.paths = append(f.paths, r.URL.Path)
	ok := len(f.script) > 0
	var rep reply
	if ok {
		rep, f.script = f.script[0], f.script[1:]
	}
	f.mu.Unlock()
	if !ok {
		http.Error(w, `{"error":{"code":"script_exhausted"}}`, http.StatusTeapot)
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

func (f *fake) requests() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...), append([]string(nil), f.paths...)
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
		Provider: "openai", Model: model, BaseURL: base, Family: "openai",
		Protocols: []registry.Protocol{registry.ProtocolOpenAIResponses},
	}
}

func newClient(t *testing.T, routes []provider.Route, fallback []string, attempts int) *provider.OpenAI {
	t.Helper()
	n := netx.New(netx.Config{})
	t.Cleanup(n.CloseIdleConnections)
	o, err := provider.NewOpenAI(n.Client("provider", netx.Inference), &provider.Config{
		Routes: routes, Fallback: fallback, Attempts: attempts, UserAgent: "damnbot/test",
		Wires: map[registry.Protocol]provider.Wire{registry.ProtocolOpenAIResponses: responses.New()},
		Wait:  func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	return o
}

func ask(text string) *provider.Request {
	return &provider.Request{
		Model:    "openai/m",
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

// TestFixtureText: a text stream becomes content with usage and the request
// goes to /responses with the Responses request shape.
func TestFixtureText(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "text.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
	resp, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Content != "Hello world" || resp.FinishReason != provider.FinishStop || resp.Model != "gpt-test" {
		t.Fatalf("response = %+v", resp)
	}
	if resp.Usage.Input != 12 || resp.Usage.Output != 3 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	reqs, paths := f.requests()
	if len(reqs) != 1 || paths[0] != "/v1/responses" {
		t.Fatalf("requests/paths = %d/%v", len(reqs), paths)
	}
	assertTextRequest(t, reqs[0])
}

// assertTextRequest checks the Responses request shape for one user turn.
func assertTextRequest(t *testing.T, raw string) {
	t.Helper()
	body := decodeBody(t, raw)
	if body["model"] != "m" || body["stream"] != true || body["store"] != false {
		t.Fatalf("body = %v", body)
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input = %v", body["input"])
	}
	msg, ok := input[0].(map[string]any)
	if !ok {
		t.Fatalf("input item = %T", input[0])
	}
	parts, ok := msg["content"].([]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("content = %v", msg["content"])
	}
	part, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("content part = %T", parts[0])
	}
	if msg["role"] != "user" || part["type"] != "input_text" || part["text"] != "hi" {
		t.Fatalf("input item = %v", msg)
	}
}

// TestFixtureUsage: cached input tokens are carried into budget.Usage.
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

// TestFixtureIncomplete: max_output_tokens maps to the length finish reason.
func TestFixtureIncomplete(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "incomplete.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
	resp, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.FinishReason != provider.FinishLength || resp.Content != "partial" {
		t.Fatalf("response = %+v", resp)
	}
}

// TestFixtureErrorEvent: an in-stream error event is a typed, retryable
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
	if pe.Class != retry.RateLimited || !pe.Class.Retryable() || pe.Code != "rate_limit_exceeded" {
		t.Fatalf("error = %+v", pe)
	}
	if len(pe.Body) == 0 {
		t.Fatal("error body is empty")
	}
}

// TestRetryAndFallback: retry stays on the route and then the shared engine
// switches to the fallback route; both speak the Responses adapter.
func TestRetryAndFallback(t *testing.T) {
	t.Parallel()
	t.Run("retry", func(t *testing.T) {
		t.Parallel()
		f := newFake(t, reply{status: http.StatusBadGateway, body: `{"error":{"code":"server_error"}}`},
			reply{raw: fixture(t, "text.sse")})
		c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 2)
		resp, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
		if err != nil || resp.Content != "Hello world" || resp.Attempts != 2 {
			t.Fatalf("retry response = %+v, %v", resp, err)
		}
	})
	t.Run("fallback", func(t *testing.T) {
		t.Parallel()
		primary := newFake(t, reply{status: http.StatusInternalServerError, body: `{"error":{"code":"server_error"}}`})
		second := newFake(t, reply{raw: fixture(t, "text.sse")})
		routes := []provider.Route{route("m", primary.URL()), route("fb", second.URL())}
		c := newClient(t, routes, []string{"openai/fb"}, 1)
		resp, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
		if err != nil || resp.Content != "Hello world" || resp.FallbackFrom != "openai/m" {
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
	if reqs, _ := f.requests(); len(reqs) != 0 {
		t.Fatalf("startup made %d requests, want 0", len(reqs))
	}
	if _, err := c.Do(t.Context(), provider.PurposeTurn, ask("ping"), nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	reqs, _ := f.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests after one turn, want 1", len(reqs))
	}
	if !strings.Contains(reqs[0], `"text":"ping"`) {
		t.Fatalf("request without user content: %s", reqs[0])
	}
}

// TestNoAdapterFailsClosed: a route whose catalog protocol has no adapter is
// refused at construction instead of being sent to the chat encoder.
func TestNoAdapterFailsClosed(t *testing.T) {
	t.Parallel()
	n := netx.New(netx.Config{})
	t.Cleanup(n.CloseIdleConnections)
	_, err := provider.NewOpenAI(n.Client("provider", netx.Inference), &provider.Config{
		Routes: []provider.Route{{
			Provider: "p", Model: "m", BaseURL: "http://127.0.0.1:9/v1",
			Protocols: []registry.Protocol{registry.ProtocolGemini},
		}},
	})
	if !errors.Is(err, provider.ErrConfig) {
		t.Fatalf("error = %v, want ErrConfig", err)
	}
}
