//go:build !minimal

package main

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// TestProtocolSelection (F5-04): a catalog preset that speaks
// openai-responses carries that protocol into the route; the F1 test/eval
// seam (--base-url) stays on openai-chat; an auth transport that is not
// wired (F5-03) is refused instead of being sent unauthenticated.
func TestProtocolSelection(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	o := &runOptions{apiKey: "sk-test"}
	route, err := resolveTest(t, &cfg, o, "xai/grok-4")
	if err != nil {
		t.Fatalf("xai: %v", err)
	}
	if len(route.Protocols) != 1 || route.Protocols[0] != registry.ProtocolOpenAIResponses {
		t.Fatalf("xai protocols = %v", route.Protocols)
	}
	route, err = resolveTest(t, &cfg, &runOptions{apiKey: "sk-test", baseURL: "http://127.0.0.1:9/v1"}, "xai/grok-4")
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	if len(route.Protocols) != 1 || route.Protocols[0] != registry.ProtocolOpenAIChat {
		t.Fatalf("override protocols = %v", route.Protocols)
	}
	if _, rerr := resolveTest(t, &cfg, o, "openai-codex/gpt-5"); rerr == nil || !strings.Contains(rerr.Error(), "F5-03") {
		t.Fatalf("oauth preset error = %v", rerr)
	}
	cfg.Providers.Custom = []config.CustomProvider{{
		Name: "kantor", BaseURL: "http://127.0.0.1:8000/v1", API: "openai-responses",
		APIKeyEnv: "KANTOR_KEY", Models: []config.CustomModel{{ID: "m"}},
	}}
	route, err = resolveTest(t, &cfg, &runOptions{}, "kantor/m")
	if err != nil {
		t.Fatalf("custom responses: %v", err)
	}
	if len(route.Protocols) != 1 || route.Protocols[0] != registry.ProtocolOpenAIResponses {
		t.Fatalf("custom protocols = %v", route.Protocols)
	}
}

// TestProtocolAnthropic (F5-05): the Messages preset carries its protocol and
// the catalog cache TTL; a custom api also accepts anthropic-messages.
func TestProtocolAnthropic(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	route, err := resolveTest(t, &cfg, &runOptions{apiKey: "sk-test"}, "anthropic/claude-3")
	if err != nil {
		t.Fatalf("anthropic: %v", err)
	}
	if len(route.Protocols) != 1 || route.Protocols[0] != registry.ProtocolAnthropicMessages || route.CacheTTLMax != 3600 {
		t.Fatalf("anthropic route = %+v", route)
	}
	cfg.Providers.Custom = []config.CustomProvider{{
		Name: "kantor", BaseURL: "http://127.0.0.1:8000/v1", API: "anthropic-messages",
		APIKeyEnv: "KANTOR_KEY", Models: []config.CustomModel{{ID: "m"}},
	}}
	route, err = resolveTest(t, &cfg, &runOptions{}, "kantor/m")
	if err != nil {
		t.Fatalf("custom messages: %v", err)
	}
	if len(route.Protocols) != 1 || route.Protocols[0] != registry.ProtocolAnthropicMessages {
		t.Fatalf("custom messages protocols = %v", route.Protocols)
	}
}
