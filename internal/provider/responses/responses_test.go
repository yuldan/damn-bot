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

// TestFixtureOversizedResponse: the assembler bound rejects a stream whose
// kept bytes exceed 16 MiB even when every frame is below the SSЕ limit.
func TestFixtureOversizedResponse(t *testing.T) {
	t.Parallel()
	chunk := strings.Repeat("x", 3<<20)
	var raw strings.Builder
	for range 6 {
		raw.WriteString(`data: {"type":"response.output_text.delta","delta":"`)
		raw.WriteString(chunk)
		raw.WriteString("\"}\n\n")
	}
	f := newFake(t, reply{raw: raw.String()})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 1)
	_, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
	if !errors.Is(err, provider.ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge", err)
	}
}

// TestExtraBodyRejected: a reserved extra_body key and a non-JSON value fail
// the request instead of silently overriding a field encode owns.
func TestExtraBodyRejected(t *testing.T) {
	t.Parallel()
	for _, extra := range []map[string]any{
		{"model": "other"},
		{"x_bad": func() {}},
	} {
		f := newFake(t, reply{raw: fixture(t, "text.sse")})
		r := route("m", f.URL())
		r.ExtraBody = extra
		c := newClient(t, []provider.Route{r}, nil, 1)
		_, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
		var pe *provider.Error
		if !errors.As(err, &pe) || pe.Class != retry.BadRequest {
			t.Fatalf("extra %v: error = %v, want BadRequest", extra, err)
		}
		if reqs, _ := f.requests(); len(reqs) != 0 {
			t.Fatalf("extra %v: %d requests sent, want 0", extra, len(reqs))
		}
	}
}

// TestExtraBodyMerged: a non-reserved extra_body key reaches the wire body.
func TestExtraBodyMerged(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "text.sse")})
	r := route("m", f.URL())
	r.ExtraBody = map[string]any{"service_tier": "flex"}
	c := newClient(t, []provider.Route{r}, nil, 0)
	if _, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	reqs, _ := f.requests()
	if !strings.Contains(reqs[0], `"service_tier":"flex"`) {
		t.Fatalf("extra_body not merged: %s", reqs[0])
	}
}

// TestOutputItemIgnored: output items that are neither function calls nor
// reasoning (message/refusal/unknown) are ignored safely. Text comes from
// deltas; an item-only stream is an empty response, not a panic.
func TestOutputItemIgnored(t *testing.T) {
	t.Parallel()
	t.Run("delta-wins", func(t *testing.T) {
		t.Parallel()
		raw := `data: {"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"item"}]}}

data: {"type":"response.output_item.done","item":{"type":"unknown","x":1}}

data: {"type":"response.output_text.delta","delta":"final"}

data: {"type":"response.completed","response":{"model":"gpt-test","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"item"}]},{"type":"unknown"}],"usage":{"input_tokens":1,"output_tokens":1}}}

`
		f := newFake(t, reply{raw: raw})
		c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
		resp, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
		if err != nil || resp.Content != "final" {
			t.Fatalf("response = %+v, %v", resp, err)
		}
	})
	t.Run("item-only-is-empty", func(t *testing.T) {
		t.Parallel()
		raw := `data: {"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"item"}]}}

data: {"type":"response.completed","response":{"model":"gpt-test","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"item"}]}]}}

`
		f := newFake(t, reply{raw: raw})
		c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 1)
		_, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
		var pe *provider.Error
		if !errors.As(err, &pe) || pe.Class != retry.EmptyResponse {
			t.Fatalf("error = %v, want empty_response", err)
		}
	})
}

// TestMalformedStream: invalid JSON fails as stream_broken whether it comes
// first or after a consumed delta, and an item that is not JSON does too.
func TestMalformedStream(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"first":  "data: not-json\n\n",
		"later":  "data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\ndata: not-json\n\n",
		"item":   "data: {\"type\":\"response.output_item.done\",\"item\":\"not-an-object\"}\n\n",
		"noterm": "data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\ndata: [DONE]\n\n",
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

// TestStreamErrorClasses: response.failed and error events map to the SPEC
// failure classes, and the error body is bounded to 64 KiB.
func TestStreamErrorClasses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		want retry.Class
	}{
		{`{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"boom"}}}`, retry.Server},
		{`{"type":"error","code":"overloaded_error","message":"busy"}`, retry.Overloaded},
		{`{"type":"error","code":"context_length_exceeded","message":"long"}`, retry.ContextLength},
		{`{"type":"error","code":"invalid_api_key","message":"key"}`, retry.Auth},
		{`{"type":"error","code":"invalid_request_error","message":"bad"}`, retry.BadRequest},
		{`{"type":"error","code":"weird_error","message":"?"}`, retry.Server},
	}
	for _, tc := range cases {
		t.Run(string(tc.want)+"-"+tc.raw[:20], func(t *testing.T) {
			t.Parallel()
			f := newFake(t, reply{raw: "data: " + tc.raw + "\n\n"})
			c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 1)
			_, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
			var pe *provider.Error
			if !errors.As(err, &pe) || pe.Class != tc.want {
				t.Fatalf("error = %v, want class %v", err, tc.want)
			}
			if tc.want != retry.Server {
				return
			}
			if len(pe.Body) > 64<<10 {
				t.Fatalf("body = %d bytes, want <= 64 KiB", len(pe.Body))
			}
		})
	}
	t.Run("truncated-body", func(t *testing.T) {
		t.Parallel()
		msg := strings.Repeat("m", 70<<10)
		f := newFake(t, reply{raw: `data: {"type":"error","code":"server_error","message":"` + msg + `"}` + "\n\n"})
		c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 1)
		_, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
		var pe *provider.Error
		if !errors.As(err, &pe) || len(pe.Body) != 64<<10 {
			t.Fatalf("error = %v, body len = %d", err, len(pe.Body))
		}
	})
}

// TestTerminalWithoutResponse: a terminal event without its response object is
// stream_broken instead of a nil dereference or a silent success.
func TestTerminalWithoutResponse(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: `data: {"type":"response.completed"}` + "\n\n"})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 1)
	_, err := c.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil)
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Class != retry.StreamBroken {
		t.Fatalf("error = %v, want stream_broken", err)
	}
}

// TestInputShapes: assistant text becomes output_text, an empty role becomes
// user, and optional sampling parameters reach the body only when set.
func TestInputShapes(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "text.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
	temp := 0.2
	req := &provider.Request{
		Model: "openai/m",
		Messages: []provider.Message{
			{Role: "assistant", Content: "hi there"},
			{Role: "", Content: "question"},
		},
		Temperature:     &temp,
		ReasoningEffort: "low",
	}
	if _, err := c.Do(t.Context(), provider.PurposeTurn, req, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	reqs, _ := f.requests()
	body := reqs[0]
	if !strings.Contains(body, `"type":"output_text"`) || !strings.Contains(body, `"text":"hi there"`) ||
		!strings.Contains(body, `"type":"input_text"`) || !strings.Contains(body, `"text":"question"`) ||
		!strings.Contains(body, `"role":"user"`) {
		t.Fatalf("input shapes = %s", body)
	}
	if !strings.Contains(body, `"temperature":0.2`) || !strings.Contains(body, `"reasoning":{"effort":"low"}`) {
		t.Fatalf("sampling parameters = %s", body)
	}
}

// TestFixtureToolCall: streamed function_call fragments assemble into one tool
// call without duplication from the done and completed events.
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
	if tc.ID != "call_1" || tc.Name != "shell" || tc.ArgumentsRaw != `{"cmd":"ls"}` {
		t.Fatalf("tool call = %+v", tc)
	}
	reqs, _ := f.requests()
	tools, ok := decodeBody(t, reqs[0])["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v", decodeBody(t, reqs[0])["tools"])
	}
	if tool, ok := tools[0].(map[string]any); !ok || tool["type"] != "function" || tool["name"] != "shell" {
		t.Fatalf("tool = %v", tools[0])
	}
}

// TestFixtureReasoningEncrypted: encrypted reasoning becomes a carrier and is
// replayed byte-identically as an input item on the next call (GAP-PROV-01).
func TestFixtureReasoningEncrypted(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "reasoning.sse")}, reply{raw: fixture(t, "text.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
	msgs := []provider.Message{{Role: "user", Content: "hi"}}
	resp, err := c.Do(t.Context(), provider.PurposeTurn, &provider.Request{Model: "openai/m", Messages: msgs}, nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Content != "Done" || resp.ReasoningText != "Think" {
		t.Fatalf("response = %+v", resp)
	}
	if len(resp.Carriers) != 1 || resp.Carriers[0].Kind != provider.CarrierEncrypted {
		t.Fatalf("carriers = %+v", resp.Carriers)
	}
	if resp.Reasoning != nil {
		t.Fatalf("reasoning not projected into carriers: %s", resp.Reasoning)
	}
	msgs = append(msgs, provider.Message{Role: "assistant", Content: resp.Content, Carriers: resp.Carriers})
	if _, err := c.Do(t.Context(), provider.PurposeTurn, &provider.Request{Model: "openai/m", Messages: msgs}, nil); err != nil {
		t.Fatalf("second Do: %v", err)
	}
	reqs, _ := f.requests()
	if !strings.Contains(reqs[1], `"encrypted_content":"enc_abc"`) || !strings.Contains(reqs[1], `"type":"reasoning"`) {
		t.Fatalf("encrypted reasoning not replayed: %s", reqs[1])
	}
}

// TestCarrierRouteMismatch: the shared carrier engine drops a block replayed
// to a different model instead of sending it to the wrong origin.
func TestCarrierRouteMismatch(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "reasoning.sse")}, reply{raw: fixture(t, "text.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL()), route("other", f.URL())}, nil, 0)
	msgs := []provider.Message{{Role: "user", Content: "hi"}}
	resp, err := c.Do(t.Context(), provider.PurposeTurn, &provider.Request{Model: "openai/m", Messages: msgs}, nil)
	if err != nil || len(resp.Carriers) != 1 {
		t.Fatalf("first Do = %+v, %v", resp, err)
	}
	msgs = append(msgs, provider.Message{Role: "assistant", Content: resp.Content, Carriers: resp.Carriers})
	second, err := c.Do(t.Context(), provider.PurposeTurn, &provider.Request{Model: "openai/other", Messages: msgs}, nil)
	if err != nil {
		t.Fatalf("second Do: %v", err)
	}
	if len(second.CarrierDrops) != 1 || second.CarrierDrops[0].Reason != "route_mismatch" {
		t.Fatalf("drops = %+v", second.CarrierDrops)
	}
	reqs, _ := f.requests()
	if strings.Contains(reqs[1], "enc_abc") {
		t.Fatalf("foreign carrier replayed: %s", reqs[1])
	}
}
