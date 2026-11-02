package main

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

func catalogForGap(t *testing.T) *registry.Catalog {
	t.Helper()
	c, err := registry.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestGAPF5_SEC24 (SPEC §22.1): a custom row may shadow a built-in id only
// with override=true; a non-default base_url for a stored credential needs
// confirmation; doctor lists every override and every non-default base_url.
func TestGAPF5_SEC24(t *testing.T) {
	t.Parallel()
	cat := catalogForGap(t)
	cfg := config.Default()
	cfg.Providers.Custom = []config.CustomProvider{
		{
			Name: "anthropic", BaseURL: "https://proxy.example/v1", API: "openai-chat", Override: true,
			Models: []config.CustomModel{{ID: "claude-x"}},
		},
		{
			Name: "openai", BaseURL: "https://openai-proxy.example/v1", API: "openai-chat", Override: true,
			Models: []config.CustomModel{{ID: "gpt-x"}},
		},
	}
	if err := validateCustomOverrides(&cfg, cat); err != nil {
		t.Fatalf("override=true rejected: %v", err)
	}
	warnings := strings.Join(providerDoctorWarnings(&cfg, cat, registry.PriceTable{}), "\n")
	for _, name := range []string{"anthropic", "openai"} {
		if !strings.Contains(warnings, `override "`+name+`"`) ||
			!strings.Contains(warnings, `override "`+name+`" uses non-default base_url`) {
			t.Errorf("doctor misses %s override/base_url:\n%s", name, warnings)
		}
	}
	// Shadowing without override is a startup error, not a silent replacement.
	cfg.Providers.Custom[0].Override = false
	if err := validateCustomOverrides(&cfg, cat); err == nil || !strings.Contains(err.Error(), "override = true") {
		t.Fatalf("shadow without flag = %v, want override error", err)
	}

	// A different base_url with an existing credential needs --confirm-base-url.
	cfg.Providers.Custom = cfg.Providers.Custom[:1]
	cfg.Providers.Custom[0].Override = true
	if _, err := resolveTest(t, &cfg, &runOptions{apiKey: "sk-test"}, "anthropic/claude-x"); err == nil ||
		!strings.Contains(err.Error(), "--confirm-base-url") {
		t.Fatalf("unconfirmed base_url = %v", err)
	}
	route, err := resolveTest(t, &cfg, &runOptions{apiKey: "sk-test", confirmBaseURL: true}, "anthropic/claude-x")
	if err != nil || route.BaseURL != "https://proxy.example/v1" {
		t.Fatalf("confirmed route = %+v, %v", route, err)
	}
}

// TestGAPF5_SEC25 (SPEC §22.1): no attribution header is sent by default for
// any catalog or local preset; the User-Agent is damnbot/<versi> without OS or
// hostname; the opt-in adds only the declared header names.
func TestGAPF5_SEC25(t *testing.T) {
	t.Parallel()
	cat := catalogForGap(t)
	cfg := config.Default()
	if cfg.Provider.SendAttribution {
		t.Fatal("send_attribution must default to false")
	}
	o := &runOptions{apiKey: "sk-test", baseURL: "https://contract.example/v1"}
	resolver := newResolver(&cfg, registry.PriceTable{}, cat, o)
	attribution := []string{"HTTP-Referer", "X-Title", "X-Client-Name"}
	checked := 0
	for _, p := range cat.Providers() {
		route, err := resolver.resolve(p.ID + "/m")
		if err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
		checked++
		for _, h := range attribution {
			if v, ok := route.Headers[h]; ok {
				t.Errorf("%s: default route carries %s=%q", p.ID, h, v)
			}
		}
	}
	if checked != cat.Len() || checked < 94 {
		t.Fatalf("checked %d presets, want all %d (88 + 6 local)", checked, cat.Len())
	}
	ua := userAgent()
	if ua != "damnbot/"+version || strings.Count(ua, "/") != 1 || strings.ContainsAny(ua, " \t\r\n") {
		t.Fatalf("User-Agent = %q, want damnbot/<versi> without OS or hostname", ua)
	}

	cfg.Provider.SendAttribution = true
	optIn := newResolver(&cfg, registry.PriceTable{}, cat, o)
	route, err := optIn.resolve("fireworks/m")
	if err != nil {
		t.Fatal(err)
	}
	if route.Headers["HTTP-Referer"] != attributionReferer || route.Headers["X-Title"] != attributionTitle {
		t.Fatalf("opt-in headers = %v", route.Headers)
	}
}
