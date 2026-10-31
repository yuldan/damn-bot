package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

func resolveTest(t *testing.T, cfg *config.Config, o *runOptions, ref string) (provider.Route, error) {
	t.Helper()
	if o == nil {
		o = &runOptions{}
	}
	cat, err := registry.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	return newResolver(cfg, registry.PriceTable{}, cat, o).resolve(ref)
}

// TestProviderResolveCustom: the 12-field custom provider resolves to a
// route without a catalog id, including env credential, model list,
// extra_headers and extra_body.
func TestProviderResolveCustom(t *testing.T) {
	t.Setenv("KANTOR_KEY", "sk-kantor")
	cfg := config.Default()
	cfg.Providers.Custom = []config.CustomProvider{{
		Name: "kantor", BaseURL: "http://127.0.0.1:8000/v1", API: "openai-chat",
		APIKeyEnv: "KANTOR_KEY", Models: []config.CustomModel{{ID: "qwen3"}},
		ExtraHeaders: map[string]string{"X-Tenant": "a"},
		ExtraBody:    map[string]any{"temperature": 0.2},
	}}
	route, err := resolveTest(t, &cfg, nil, "kantor/qwen3")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if route.Provider != "kantor" || route.Model != "qwen3" || route.BaseURL != "http://127.0.0.1:8000/v1" {
		t.Fatalf("route = %+v", route)
	}
	if route.APIKey != "sk-kantor" || route.Family != "http://127.0.0.1:8000/v1" {
		t.Fatalf("key/family = %q/%q", route.APIKey, route.Family)
	}
	if route.Headers["X-Tenant"] != "a" || route.ExtraBody["temperature"] != 0.2 {
		t.Fatalf("headers/body = %v/%v", route.Headers, route.ExtraBody)
	}
	if _, err := resolveTest(t, &cfg, nil, "kantor/unknown"); err == nil {
		t.Fatal("model outside models[] resolved")
	}
}

// TestOverrideWithoutFlag: a custom id that shadows a built-in fails without
// override=true; with it, a non-default base_url needs --confirm-base-url.
func TestOverrideWithoutFlag(t *testing.T) {
	cat, err := registry.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	base := config.CustomProvider{
		Name: "anthropic", BaseURL: "https://proxy.example.com", API: "openai-chat",
		APIKeyEnv: "ANTHROPIC" + "_API_KEY", Models: []config.CustomModel{{ID: "claude"}},
	}
	cfg := config.Default()
	cfg.Providers.Custom = []config.CustomProvider{base}
	if err := validateCustomOverrides(&cfg, cat); err == nil {
		t.Fatal("shadowing provider without override was accepted")
	}
	if _, err := resolveTest(t, &cfg, nil, "anthropic/claude"); err == nil {
		t.Fatal("resolver accepted a shadowing provider without override")
	}
	cfg.Providers.Custom[0].Override = true
	if _, err := resolveTest(t, &cfg, nil, "anthropic/claude"); err == nil {
		t.Fatal("non-default base_url accepted without --confirm-base-url")
	}
	if _, err := resolveTest(t, &cfg, &runOptions{confirmBaseURL: true}, "anthropic/claude"); err != nil {
		t.Fatalf("confirmed override: %v", err)
	}
	// The built-in base_url needs no confirmation.
	cfg.Providers.Custom[0].BaseURL = "https://api.anthropic.com"
	if _, err := resolveTest(t, &cfg, nil, "anthropic/claude"); err != nil {
		t.Fatalf("built-in base_url override: %v", err)
	}
}

// TestNoAttributionHeaders: send_attribution=false (default) yields no
// attribution header for any catalog preset; the opt-in adds only the
// declared names.
func TestNoAttributionHeaders(t *testing.T) {
	cat, err := registry.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	o := &runOptions{apiKey: "sk-test"}
	attribution := []string{"HTTP-Referer", "X-Title", "X-Client-Name"}
	checked := 0
	for _, p := range cat.Providers() {
		if p.Local || p.BaseURL == "" || strings.ContainsAny(p.BaseURL, "<>{") || !hasProtocol(p.Protocols, registry.ProtocolOpenAIChat) {
			continue
		}
		route, rerr := newResolver(&cfg, registry.PriceTable{}, cat, o).resolve(p.ID + "/m")
		if rerr != nil {
			continue // a preset that needs a custom base_url is covered elsewhere
		}
		checked++
		for _, h := range attribution {
			if v, ok := route.Headers[h]; ok {
				t.Fatalf("%s: default route carries %s=%q", p.ID, h, v)
			}
		}
	}
	if checked < 60 {
		t.Fatalf("checked %d presets, want at least 60", checked)
	}
	cfg.Provider.SendAttribution = true
	route, err := newResolver(&cfg, registry.PriceTable{}, cat, o).resolve("fireworks/m")
	if err != nil {
		t.Fatal(err)
	}
	if route.Headers["HTTP-Referer"] != attributionReferer || route.Headers["X-Title"] != attributionTitle {
		t.Fatalf("opt-in headers = %v", route.Headers)
	}
}

func instantWait(context.Context, time.Duration) error { return nil }

// TestFallbackWiredFromConfig: cfg.Provider.Fallback reaches the existing
// provider chain route-for-route and the client actually switches.
func TestFallbackWiredFromConfig(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Provider.Fallback = []string{"deepseek/fallback"}
	cat, err := registry.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	o := &runOptions{apiKey: "sk-test"}
	routes, fallback, err := newResolver(&cfg, registry.PriceTable{}, cat, o).routes("openrouter/primary")
	if err != nil {
		t.Fatalf("routes: %v", err)
	}
	if len(routes) != 2 || routes[1].Ref() != "deepseek/fallback" || len(fallback) != 1 || fallback[0] != "deepseek/fallback" {
		t.Fatalf("routes/fallback = %v/%v", routes, fallback)
	}

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"code":"server_error"}}`, http.StatusInternalServerError)
	}))
	t.Cleanup(primary.Close)
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if _, werr := w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"fb\"}}]}\n\n" +
			"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")); werr != nil {
			t.Errorf("write reply: %v", werr)
		}
	}))
	t.Cleanup(second.Close)
	for i := range routes {
		routes[i].BaseURL = primary.URL
	}
	routes[1].BaseURL = second.URL

	n := netx.New(netx.Config{})
	t.Cleanup(n.CloseIdleConnections)
	client, err := provider.NewOpenAI(n.Client("provider", netx.Inference), &provider.Config{
		Routes: routes, Fallback: fallback, Attempts: 1, Wait: instantWait, UserAgent: "damnbot/test",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(t.Context(), provider.PurposeTurn, &provider.Request{
		Model: "openrouter/primary", Messages: []provider.Message{{Role: "user", Content: "hi"}},
	}, nil)
	if err != nil || resp.FallbackFrom != "openrouter/primary" || resp.Content != "fb" {
		t.Fatalf("fallback reply = %+v, %v", resp, err)
	}
}

// TestLightUsedForCompaction: provider.light is registered as an auxiliary
// route and selected only for purpose=compaction; the main turn keeps its
// own ref.
func TestLightUsedForCompaction(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Provider.Light = "deepseek/light"
	cat, err := registry.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	routes, fallback, err := newResolver(&cfg, registry.PriceTable{}, cat, &runOptions{apiKey: "sk"}).routes("openrouter/primary")
	if err != nil {
		t.Fatal(err)
	}
	ref := lightRoute(&cfg, "openrouter/primary", &routes, newResolver(&cfg, registry.PriceTable{}, cat, &runOptions{apiKey: "sk"}), fallback)
	if ref != "deepseek/light" || len(routes) != 2 || routes[1].Ref() != "deepseek/light" {
		t.Fatalf("light = %q routes = %v", ref, routes)
	}
	adapter := &providerAdapter{model: "openrouter/primary", lightRef: ref}
	if prov, model := adapter.routeModel(provider.PurposeTurn, "openrouter/primary"); prov != "openrouter" || model != "openrouter/primary" {
		t.Fatalf("turn route = %q/%q", prov, model)
	}
	if prov, model := adapter.routeModel(provider.PurposeCompaction, "openrouter/primary"); prov != "deepseek" || model != "deepseek/light" {
		t.Fatalf("compaction route = %q/%q", prov, model)
	}
}

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
