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
}
