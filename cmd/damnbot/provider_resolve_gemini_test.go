//go:build !minimal

package main

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// TestProtocolGemini (F5-06): a custom provider can select the native Gemini
// adapter, while a catalog preset that speaks gemini still needs the F5-03
// OAuth transport instead of being sent unauthenticated.
func TestProtocolGemini(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Providers.Custom = []config.CustomProvider{{
		Name: "studio", BaseURL: "https://generativelanguage.googleapis.com/v1beta", API: "gemini",
		APIKeyEnv: "STUDIO_KEY", Models: []config.CustomModel{{ID: "gemini-2.5-pro"}},
	}}
	route, err := resolveTest(t, &cfg, &runOptions{}, "studio/gemini-2.5-pro")
	if err != nil {
		t.Fatalf("custom gemini: %v", err)
	}
	if len(route.Protocols) != 1 || route.Protocols[0] != registry.ProtocolGemini {
		t.Fatalf("custom gemini protocols = %v", route.Protocols)
	}
	if _, rerr := resolveTest(t, &cfg, &runOptions{apiKey: "sk-test"}, "google-gemini-cli/gemini-2.5-pro"); rerr == nil || !strings.Contains(rerr.Error(), "F5-03") {
		t.Fatalf("oauth gemini preset error = %v", rerr)
	}
}
