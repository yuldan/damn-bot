package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// wireMessage is the decoded shape of one wire message.
type wireMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// anthropicRoute is the route the runner builds for an Anthropic origin.
func anthropicRoute() *Route {
	return &Route{Provider: "anthropic", Model: "claude", Family: "anthropic"}
}

// boundaryRequest is a request whose message at position after is the cache
// breakpoint (after 0 disables the breakpoint).
func boundaryRequest(after int) *Request {
	return &Request{
		Model: "claude", MaxTokens: 16, CacheBreakAfter: after,
		Messages: []Message{{Role: "system", Content: "stable prefix"}, {Role: "user", Content: "hi"}},
	}
}

// encodeAnthropic encodes req for the Anthropic route, failing the test unless
// the wire body is valid JSON.
func encodeAnthropic(t *testing.T, req *Request) []byte {
	t.Helper()
	body, err := encode(anthropicRoute(), req, nil)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !json.Valid(body) {
		t.Fatalf("encode: invalid JSON:\n%s", body)
	}
	return body
}

// decodeMessages decodes the message list of a wire body.
func decodeMessages(t *testing.T, body []byte) []wireMessage {
	t.Helper()
	var decoded struct {
		Messages []wireMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.Messages
}

// assertNoCacheControl fails the test when the encoded body carries the
// ephemeral cache marker.
func assertNoCacheControl(t *testing.T, name string, rt *Route, req *Request) {
	t.Helper()
	body, err := encode(rt, req, nil)
	if err != nil {
		t.Fatalf("%s: encode: %v", name, err)
	}
	if strings.Contains(string(body), "cache_control") {
		t.Errorf("%s emitted cache_control:\n%s", name, body)
	}
}

// TestCacheBreakpointAnthropic: an anthropic-family route marks the boundary
// message with cache_control; other families keep the wire body unchanged.
func TestCacheBreakpointAnthropic(t *testing.T) {
	t.Parallel()
	body := encodeAnthropic(t, boundaryRequest(1))
	t.Run("marks the boundary block", func(t *testing.T) {
		t.Parallel()
		assertBoundaryMarkers(t, body)
	})
	t.Run("keeps the rest of the body intact", func(t *testing.T) {
		t.Parallel()
		assertBoundaryBlock(t, body)
	})
	t.Run("other families and breakpoints stay inert", func(t *testing.T) {
		t.Parallel()
		assertInertBreakpoints(t)
	})
}

// assertBoundaryMarkers checks the anthropic body carries the marker substring
// exactly once.
func assertBoundaryMarkers(t *testing.T, body []byte) {
	t.Helper()
	for _, want := range []string{`"cache_control":{"type":"ephemeral"}`, `"type":"text"`, "stable prefix"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("anthropic body lacks %q:\n%s", want, body)
		}
	}
	if n := strings.Count(string(body), "cache_control"); n != 1 {
		t.Errorf("got %d cache_control markers, want exactly 1:\n%s", n, body)
	}
}

// assertBoundaryBlock checks the breakpoint rides the boundary message as a
// single ephemeral text block, leaving the message after it untouched.
func assertBoundaryBlock(t *testing.T, body []byte) {
	t.Helper()
	msgs := decodeMessages(t, body)
	if len(msgs) != 2 {
		t.Fatalf("decoded %d messages, want 2", len(msgs))
	}
	blocks, isList := msgs[0].Content.([]any)
	if !isList || len(blocks) != 1 {
		t.Fatalf("boundary message is not a single content block: %#v", msgs[0].Content)
	}
	block, isObject := blocks[0].(map[string]any)
	if !isObject {
		t.Fatalf("boundary block is not an object: %#v", blocks[0])
	}
	if block["type"] != "text" || block["text"] != "stable prefix" {
		t.Errorf("boundary block = %#v", block)
	}
	cc, isObject := block["cache_control"].(map[string]any)
	if !isObject || cc["type"] != "ephemeral" {
		t.Errorf("boundary cache_control = %#v", block["cache_control"])
	}
	if _, isText := msgs[1].Content.(string); !isText {
		t.Errorf("message after the boundary changed: %#v", msgs[1].Content)
	}
}

// uncachedCase is one route/request pair that must not emit a cache marker.
type uncachedCase struct {
	name  string
	route *Route
	req   *Request
}

// uncachedCases are the routes and breakpoints that stay inert: a non-anthropic
// family, breakpoint 0, a boundary message carrying tool calls (it keeps its
// string content) and a boundary past the end of the message list.
func uncachedCases() []uncachedCase {
	return []uncachedCase{
		{"generic family", &Route{Provider: "p", Model: "m", Family: "openai"}, boundaryRequest(1)},
		{"zero breakpoint", anthropicRoute(), boundaryRequest(0)},
		{"tool-call boundary", anthropicRoute(), &Request{
			MaxTokens: 16, CacheBreakAfter: 1,
			Messages: []Message{{
				Role: "assistant", Content: "x",
				ToolCalls: []ToolCall{{ID: "c1", Name: "t", ArgumentsRaw: "{}"}},
			}},
		}},
		{"boundary past the list", anthropicRoute(), &Request{
			MaxTokens: 16, CacheBreakAfter: 5,
			Messages: []Message{{Role: "system", Content: "s"}},
		}},
	}
}

// assertInertBreakpoints checks every uncached case encodes without the marker.
func assertInertBreakpoints(t *testing.T) {
	t.Helper()
	for _, c := range uncachedCases() {
		assertNoCacheControl(t, c.name, c.route, c.req)
	}
}

// TestCacheBreakpointProductionFamily: the family the runner actually derives
// from `--base-url`/config (NormalizeBaseURL, SEC8-9) is recognised for an
// Anthropic origin, so the production path emits the breakpoint; the same shape
// for another origin stays inert.
func TestCacheBreakpointProductionFamily(t *testing.T) {
	t.Parallel()
	req := boundaryRequest(1)
	rt := &Route{Provider: "anthropic", Model: "claude", Family: baseFamily(t, "https://api.anthropic.com/v1")}
	body, err := encode(rt, req, nil)
	if err != nil || strings.Count(string(body), "cache_control") != 1 {
		t.Fatalf("production family %q: %s %v", rt.Family, body, err)
	}
	if !strings.Contains(string(body), `"cache_control":{"type":"ephemeral"}`) || !strings.Contains(string(body), "stable prefix") {
		t.Errorf("production family body lacks the boundary block:\n%s", body)
	}
	other := &Route{Provider: "openai", Model: "m", Family: baseFamily(t, "http://127.0.0.1:8080/v1")}
	if got, gerr := encode(other, req, nil); gerr != nil || strings.Contains(string(got), "cache_control") {
		t.Errorf("non-anthropic origin emitted cache_control: %s %v", got, gerr)
	}
}

// baseFamily is the route family the runner derives from one base URL.
func baseFamily(t *testing.T, base string) string {
	t.Helper()
	f, err := NormalizeBaseURL(base)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestCacheBreakStableBody: a request the route cannot cache is byte-identical
// to the pre-MEM-034 encoding (same field order, empty string content kept,
// tool_calls untouched) for every non-anthropic family and for breakpoint 0.
func TestCacheBreakStableBody(t *testing.T) {
	t.Parallel()
	req := &Request{
		Model: "m", MaxTokens: 16, CacheBreakAfter: 1,
		Messages: []Message{
			{Role: "system", Content: "stable <&> prefix"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "t", ArgumentsRaw: "{}"}}},
			{Role: "user", Content: `hi "q"`},
		},
	}
	const want = `{"model":"m","messages":[` +
		`{"role":"system","content":"stable <&> prefix"},` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"t","arguments":"{}"}}]},` +
		`{"role":"user","content":"hi \"q\""}],` +
		`"max_tokens":16,"stream":true,"stream_options":{"include_usage":true}}`
	for _, rt := range []*Route{
		{Provider: "p", Model: "m", Family: "openai"},
		// The run wiring sets Family to the normalized base URL (SEC8-9): a
		// non-Anthropic origin must keep the pre-MEM-034 bytes.
		{Provider: "p", Model: "m", Family: baseFamily(t, "http://127.0.0.1:8080/v1")},
		{Provider: "p", Model: "m"},
	} {
		body, err := encode(rt, req, nil)
		if err != nil || string(body) != want {
			t.Errorf("family %q:\ngot  %s\nwant %s (%v)", rt.Family, body, want, err)
		}
	}
	zero := *req
	zero.CacheBreakAfter = 0
	body, err := encode(&Route{Provider: "anthropic", Model: "m", Family: "anthropic"}, &zero, nil)
	if err != nil || string(body) != want {
		t.Errorf("zero breakpoint:\ngot  %s\nwant %s (%v)", body, want, err)
	}
}

// TestCacheTTLByOrigin (GAP-COST-03): the openai-chat Anthropic dialect gets
// the 1h breakpoint only for an interactive origin and only when the catalog
// claims it; every other combination stays on the 5m default.
func TestCacheTTLByOrigin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		origin string
		ttlMax int64
		want   string
	}{
		{"interactive", "tui", 3600, `"ttl":"1h"`},
		{"interactive-acp", "acp", 3600, `"ttl":"1h"`},
		{"batch-origin", "cron", 3600, `"cache_control":{"type":"ephemeral"}`},
		{"clamped", "tui", 300, `"cache_control":{"type":"ephemeral"}`},
		{"no-catalog", "tui", 0, `"cache_control":{"type":"ephemeral"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rt := anthropicRoute()
			rt.CacheTTLMax = tc.ttlMax
			req := boundaryRequest(1)
			req.Origin = tc.origin
			body, err := encode(rt, req, nil)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if !strings.Contains(string(body), tc.want) {
				t.Fatalf("body lacks %q:\n%s", tc.want, body)
			}
			if tc.ttlMax < 3600 || tc.origin == "cron" {
				if strings.Contains(string(body), `"ttl"`) {
					t.Fatalf("ttl sent below the catalog claim:\n%s", body)
				}
			}
		})
	}
}
