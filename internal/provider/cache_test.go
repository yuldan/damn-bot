package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCacheBreakpointAnthropic: an anthropic-family route marks the boundary
// message with cache_control; other families keep the wire body unchanged.
func TestCacheBreakpointAnthropic(t *testing.T) {
	t.Parallel()
	req := &Request{
		Model: "claude", MaxTokens: 16, CacheBreakAfter: 1,
		Messages: []Message{{Role: "system", Content: "stable prefix"}, {Role: "user", Content: "hi"}},
	}
	body, err := encode(&Route{Provider: "anthropic", Model: "claude", Family: "anthropic"}, req, nil)
	if err != nil || !json.Valid(body) {
		t.Fatalf("encode: %s %v", body, err)
	}
	for _, want := range []string{`"cache_control":{"type":"ephemeral"}`, `"type":"text"`, "stable prefix"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("anthropic body lacks %q:\n%s", want, body)
		}
	}
	if n := strings.Count(string(body), "cache_control"); n != 1 {
		t.Errorf("got %d cache_control markers, want exactly 1:\n%s", n, body)
	}
	var decoded struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	blocks, ok := decoded.Messages[0].Content.([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("boundary message is not a single content block: %#v", decoded.Messages[0].Content)
	}
	block, _ := blocks[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "stable prefix" {
		t.Errorf("boundary block = %#v", block)
	}
	if cc, _ := block["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Errorf("boundary cache_control = %#v", block["cache_control"])
	}
	if _, ok := decoded.Messages[1].Content.(string); !ok {
		t.Errorf("message after the boundary changed: %#v", decoded.Messages[1].Content)
	}
	generic, err := encode(&Route{Provider: "p", Model: "m", Family: "openai"}, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(generic), "cache_control") {
		t.Errorf("generic family emitted cache_control:\n%s", generic)
	}
	req.CacheBreakAfter = 0
	none, err := encode(&Route{Provider: "anthropic", Model: "claude", Family: "anthropic"}, req, nil)
	if err != nil || strings.Contains(string(none), "cache_control") {
		t.Errorf("zero breakpoint emitted cache_control: %s %v", none, err)
	}
	// A boundary message carrying tool calls keeps its string content, and a
	// boundary past the end of the message list is a no-op.
	for _, c := range []struct {
		name string
		req  *Request
	}{
		{"tool-call boundary", &Request{MaxTokens: 16, CacheBreakAfter: 1, Messages: []Message{
			{Role: "assistant", Content: "x", ToolCalls: []ToolCall{{ID: "c1", Name: "t", ArgumentsRaw: "{}"}}}}}},
		{"boundary past the list", &Request{MaxTokens: 16, CacheBreakAfter: 5, Messages: []Message{
			{Role: "system", Content: "s"}}}},
	} {
		got, cerr := encode(&Route{Provider: "anthropic", Model: "claude", Family: "anthropic"}, c.req, nil)
		if cerr != nil || strings.Contains(string(got), "cache_control") {
			t.Errorf("%s emitted cache_control: %s %v", c.name, got, cerr)
		}
	}
}

// TestCacheBreakpointProductionFamily: the family the runner actually derives
// from `--base-url`/config (NormalizeBaseURL, SEC8-9) is recognised for an
// Anthropic origin, so the production path emits the breakpoint; the same shape
// for another origin stays inert.
func TestCacheBreakpointProductionFamily(t *testing.T) {
	t.Parallel()
	req := &Request{
		Model: "claude", MaxTokens: 16, CacheBreakAfter: 1,
		Messages: []Message{{Role: "system", Content: "stable prefix"}, {Role: "user", Content: "hi"}},
	}
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
