//go:build !minimal

package anthropic_test

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
	"github.com/damnbot/damnbot/internal/provider/anthropic"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// reply is one scripted fake Messages response.
type reply struct {
	status int
	body   string
	raw    string
}

// fake is a loopback /v1/messages server that replays fixtures and records
// every request body and header, so a test can prove the adapter sends
// nothing extra.
type fake struct {
	srv    *httptest.Server
	mu     sync.Mutex
	script []reply
	reqs   []string
	paths  []string
	hdrs   []http.Header
}

func newFake(t *testing.T, script ...reply) *fake {
	t.Helper()
	f := &fake{script: script}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) URL() string { return f.srv.URL }

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	body, rerr := io.ReadAll(r.Body)
	if rerr != nil {
		http.Error(w, `{"type":"error","error":{"type":"invalid_request_error"}}`, http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, string(body))
	f.paths = append(f.paths, r.URL.Path)
	f.hdrs = append(f.hdrs, r.Header.Clone())
	ok := len(f.script) > 0
	var rep reply
	if ok {
		rep, f.script = f.script[0], f.script[1:]
	}
	f.mu.Unlock()
	if !ok {
		http.Error(w, `{"type":"error","error":{"type":"api_error","message":"exhausted"}}`, http.StatusTeapot)
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
	return append([]string(nil), f.reqs...), append([]string(nil), f.paths...), append([]http.Header(nil), f.hdrs...)
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
		Provider: "anthropic", Model: model, BaseURL: base, Family: "anthropic", APIKey: "test-key",
		Protocols: []registry.Protocol{registry.ProtocolAnthropicMessages},
	}
}

func newClient(t *testing.T, routes []provider.Route, fallback []string, attempts int) *provider.OpenAI {
	t.Helper()
	n := netx.New(netx.Config{})
	t.Cleanup(n.CloseIdleConnections)
	o, err := provider.NewOpenAI(n.Client("provider", netx.Inference), &provider.Config{
		Routes: routes, Fallback: fallback, Attempts: attempts, UserAgent: "damnbot/test",
		Wires: map[registry.Protocol]provider.Wire{registry.ProtocolAnthropicMessages: anthropic.New()},
		Wait:  func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	return o
}

func ask(text string) *provider.Request {
	return &provider.Request{Model: "anthropic/m", Messages: []provider.Message{{Role: "user", Content: text}}}
}

func decodeBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("request body %s: %v", raw, err)
	}
	return body
}

// TestFixtureText: a text stream becomes content with usage, and the request
// goes to /v1/messages with the Messages shape.
func TestFixtureText(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "text.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
	resp, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Content != "Hello world" || resp.FinishReason != provider.FinishStop || resp.Model != "claude-test" {
		t.Fatalf("response = %+v", resp)
	}
	if resp.Usage.Input != 12 || resp.Usage.Output != 3 || resp.Usage.CachedInput != 0 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	reqs, paths, _ := f.requests()
	if len(reqs) != 1 || paths[0] != "/v1/messages" {
		t.Fatalf("requests/paths = %d/%v", len(reqs), paths)
	}
	body := decodeBody(t, reqs[0])
	if body["model"] != "m" || body["stream"] != true || body["max_tokens"] == nil {
		t.Fatalf("body = %v", body)
	}
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages = %v", body["messages"])
	}
}

// TestFixtureUsage: cache read/write tokens normalize into budget.Usage,
// with Input = uncached + read + write (the total prompt).
func TestFixtureUsage(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "usage.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
	resp, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	u := resp.Usage
	if u.Input != 80 || u.CachedInput != 40 || u.CacheCreation != 30 || u.Output != 5 {
		t.Fatalf("usage = %+v", u)
	}
}

// TestFixtureToolCall: partial_json fragments assemble into one tool call.
func TestFixtureToolCall(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "tool.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
	req := ask("ls")
	req.Tools = []provider.ToolSchema{{Name: "shell", Description: "run", Parameters: json.RawMessage(`{"type":"object"}`)}}
	resp, err := c.Do(t.Context(), provider.PurposeTurn, req, nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.FinishReason != provider.FinishToolCalls || len(resp.ToolCalls) != 1 {
		t.Fatalf("finish/tools = %q/%+v", resp.FinishReason, resp.ToolCalls)
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "toolu_1" || tc.Name != "shell" || tc.ArgumentsRaw != `{"cmd":"ls"}` {
		t.Fatalf("tool call = %+v", tc)
	}
	reqs, _, _ := f.requests()
	tool, ok := decodeBody(t, reqs[0])["tools"].([]any)
	if !ok || len(tool) != 1 {
		t.Fatalf("tools = %v", decodeBody(t, reqs[0])["tools"])
	}
	if m, ok := tool[0].(map[string]any); !ok || m["name"] != "shell" || m["input_schema"] == nil {
		t.Fatalf("tool = %v", tool[0])
	}
}

// TestFixtureThinkingRedacted: thinking and redacted_thinking become
// replayable carriers; the assistant turn replays them byte-identically.
func TestFixtureThinkingRedacted(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "thinking.sse")}, reply{raw: fixture(t, "text.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
	msgs := []provider.Message{{Role: "user", Content: "hi"}}
	resp, err := c.Do(t.Context(), provider.PurposeTurn, &provider.Request{Model: "anthropic/m", Messages: msgs}, nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Content != "Done" || resp.ReasoningText != "I think" {
		t.Fatalf("response = %+v", resp)
	}
	if len(resp.Carriers) != 2 {
		t.Fatalf("carriers = %+v", resp.Carriers)
	}
	if resp.Carriers[0].Kind != provider.CarrierThinking || resp.Carriers[1].Kind != provider.CarrierRedactedThinking {
		t.Fatalf("carrier kinds = %v/%v", resp.Carriers[0].Kind, resp.Carriers[1].Kind)
	}
	if resp.Reasoning != nil {
		t.Fatalf("reasoning not projected into carriers: %s", resp.Reasoning)
	}
	msgs = append(msgs, provider.Message{Role: "assistant", Content: resp.Content, Carriers: resp.Carriers})
	if _, err := c.Do(t.Context(), provider.PurposeTurn, &provider.Request{Model: "anthropic/m", Messages: msgs}, nil); err != nil {
		t.Fatalf("second Do: %v", err)
	}
	reqs, _, _ := f.requests()
	for _, want := range []string{`"type":"thinking"`, `"signature":"sig_abc"`, `"type":"redacted_thinking"`, `"data":"redacted_blob"`} {
		if !strings.Contains(reqs[1], want) {
			t.Fatalf("carrier %s not replayed: %s", want, reqs[1])
		}
	}
}

// TestFixtureIncomplete: max_tokens maps to the length finish reason.
func TestFixtureIncomplete(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "incomplete.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
	resp, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
	if err != nil || resp.FinishReason != provider.FinishLength || resp.Content != "partial" {
		t.Fatalf("response = %+v, %v", resp, err)
	}
}

// TestFixtureErrorEvent: an in-stream error event is a typed, retryable
// provider error.
func TestFixtureErrorEvent(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "error.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 1)
	_, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
	var pe *provider.Error
	if !errors.As(err, &pe) {
		t.Fatalf("error = %v, want *provider.Error", err)
	}
	if pe.Class != retry.Overloaded || !pe.Class.Retryable() || pe.Code != "overloaded_error" {
		t.Fatalf("error = %+v", pe)
	}
	if len(pe.Body) == 0 {
		t.Fatal("error body is empty")
	}
}

// TestRetryAndFallback: retry stays on the route and then the shared engine
// switches to the fallback route; both speak Messages.
func TestRetryAndFallback(t *testing.T) {
	t.Parallel()
	t.Run("retry", func(t *testing.T) {
		t.Parallel()
		f := newFake(t, reply{status: http.StatusBadGateway, body: `{"type":"error","error":{"type":"api_error"}}`},
			reply{raw: fixture(t, "text.sse")})
		c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 2)
		resp, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
		if err != nil || resp.Content != "Hello world" || resp.Attempts != 2 {
			t.Fatalf("retry response = %+v, %v", resp, err)
		}
	})
	t.Run("fallback", func(t *testing.T) {
		t.Parallel()
		primary := newFake(t, reply{status: http.StatusInternalServerError, body: `{"type":"error","error":{"type":"api_error"}}`})
		second := newFake(t, reply{raw: fixture(t, "text.sse")})
		routes := []provider.Route{route("m", primary.URL()), route("fb", second.URL())}
		c := newClient(t, routes, []string{"anthropic/fb"}, 1)
		resp, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
		if err != nil || resp.Content != "Hello world" || resp.FallbackFrom != "anthropic/m" {
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
	if len(reqs) != 1 || !strings.Contains(reqs[0], `"text":"ping"`) {
		t.Fatalf("requests after one turn = %d: %v", len(reqs), reqs)
	}
}

// TestCacheTTLByOrigin (GAP-COST-03): an interactive origin asks for the 1h
// breakpoint when the catalog allows it; every other origin keeps the 5m
// default, and cache_ttl_max clamps a one-hour request.
func TestCacheTTLByOrigin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		origin string
		ttlMax int64
		want1h bool
	}{
		{"tui-hour", "tui", 3600, true},
		{"acp-hour", "acp", 7200, true},
		{"web-hour", "web", 3600, true},
		{"gateway-hour", "gateway", 3600, true},
		{"cron-five-minutes", "cron", 3600, false},
		{"headless-five-minutes", "headless", 3600, false},
		{"delegate-five-minutes", "delegate", 3600, false},
		{"clamped-to-catalog", "tui", 300, false},
		{"catalog-none", "tui", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFake(t, reply{raw: fixture(t, "text.sse")})
			r := route("m", f.URL())
			r.CacheTTLMax = tc.ttlMax
			c := newClient(t, []provider.Route{r}, nil, 0)
			req := ask("hi")
			req.CacheBreakAfter = 1
			req.Origin = tc.origin
			if _, err := c.Do(t.Context(), provider.PurposeTurn, req, nil); err != nil {
				t.Fatalf("Do: %v", err)
			}
			reqs, _, hdrs := f.requests()
			has1h := strings.Contains(reqs[0], `"ttl":"1h"`) && strings.Contains(reqs[0], `"type":"ephemeral"`)
			if has1h != tc.want1h {
				t.Fatalf("1h TTL = %v, want %v: %s", has1h, tc.want1h, reqs[0])
			}
			if n := strings.Count(reqs[0], "cache_control"); n != 1 {
				t.Fatalf("%d cache_control markers, want 1: %s", n, reqs[0])
			}
			if got := hdrs[0].Get("anthropic-beta"); (got != "") != tc.want1h {
				t.Fatalf("anthropic-beta = %q, want set=%v", got, tc.want1h)
			}
		})
	}
}

// TestTransportHeaders: Messages uses x-api-key and the version header, never
// an Authorization bearer.
func TestTransportHeaders(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "text.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
	if _, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _, hdrs := f.requests()
	if hdrs[0].Get("x-api-key") != "test-key" || hdrs[0].Get("anthropic-version") != anthropic.Version {
		t.Fatalf("headers = %v", hdrs[0])
	}
	if hdrs[0].Get("Authorization") != "" {
		t.Fatalf("bearer sent to Messages: %v", hdrs[0])
	}
}

// TestEncodeShapes: system messages become the top-level system array, the
// boundary carries cache_control, thinking is enabled from reasoning_effort,
// and temperature is omitted while thinking is on.
func TestEncodeShapes(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "text.sse")})
	r := route("m", f.URL())
	r.CacheTTLMax = 3600
	c := newClient(t, []provider.Route{r}, nil, 0)
	temp := 0.2
	req := &provider.Request{
		Model: "anthropic/m", MaxTokens: 8192, CacheBreakAfter: 1, Origin: "tui",
		Messages: []provider.Message{
			{Role: "system", Content: "stable prefix"},
			{Role: "user", Content: "hi"},
		},
		Temperature:     &temp,
		ReasoningEffort: "high",
	}
	if _, err := c.Do(t.Context(), provider.PurposeTurn, req, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	reqs, _, _ := f.requests()
	body := decodeBody(t, reqs[0])
	sys, ok := body["system"].([]any)
	if !ok || len(sys) != 1 || !strings.Contains(reqs[0], `"ttl":"1h"`) {
		t.Fatalf("system/cache = %v: %s", body["system"], reqs[0])
	}
	if body["thinking"] == nil || strings.Contains(reqs[0], `"temperature"`) {
		t.Fatalf("thinking/temperature = %s", reqs[0])
	}
	if msgs, ok := body["messages"].([]any); !ok || len(msgs) != 1 {
		t.Fatalf("messages = %v", body["messages"])
	}
}

// TestCarrierRouteMismatch: the shared carrier engine drops a block replayed
// to a different model instead of sending it to the wrong origin.
func TestCarrierRouteMismatch(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "thinking.sse")}, reply{raw: fixture(t, "text.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL()), route("other", f.URL())}, nil, 0)
	msgs := []provider.Message{{Role: "user", Content: "hi"}}
	resp, err := c.Do(t.Context(), provider.PurposeTurn, &provider.Request{Model: "anthropic/m", Messages: msgs}, nil)
	if err != nil || len(resp.Carriers) != 2 {
		t.Fatalf("first Do = %+v, %v", resp, err)
	}
	msgs = append(msgs, provider.Message{Role: "assistant", Content: resp.Content, Carriers: resp.Carriers})
	second, err := c.Do(t.Context(), provider.PurposeTurn, &provider.Request{Model: "anthropic/other", Messages: msgs}, nil)
	if err != nil {
		t.Fatalf("second Do: %v", err)
	}
	if len(second.CarrierDrops) != 1 || second.CarrierDrops[0].Reason != "route_mismatch" || second.CarrierDrops[0].Count != 2 {
		t.Fatalf("drops = %+v", second.CarrierDrops)
	}
	reqs, _, _ := f.requests()
	if strings.Contains(reqs[1], "sig_abc") || strings.Contains(reqs[1], "redacted_blob") {
		t.Fatalf("foreign carrier replayed: %s", reqs[1])
	}
}

// TestMalformedStream: invalid JSON is stream_broken, a missing message_stop
// is stream_broken, and an empty turn is empty_response.
func TestMalformedStream(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"not-json": "data: not-json\n\n",
		"no-stop":  "data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-test\"}}\n\n",
		"no-finish": "data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-test\"}}\n\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFake(t, reply{raw: raw})
			c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 1)
			_, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
			var pe *provider.Error
			if !errors.As(err, &pe) || pe.Class != retry.StreamBroken {
				t.Fatalf("error = %v, want stream_broken", err)
			}
		})
	}
}

// TestEmptyResponse: a successful stop with no text and no tool call is the
// typed empty_response, not a silent success.
func TestEmptyResponse(t *testing.T) {
	t.Parallel()
	raw := `data: {"type":"message_start","message":{"id":"m","model":"claude-test","usage":{"input_tokens":1,"output_tokens":1}}}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":0}}

data: {"type":"message_stop"}

`
	f := newFake(t, reply{raw: raw})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 1)
	_, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Class != retry.EmptyResponse {
		t.Fatalf("error = %v, want empty_response", err)
	}
}
