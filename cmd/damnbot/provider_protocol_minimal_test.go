//go:build minimal

package main

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// TestProtocolSelection (F5-04, minimal): the minimal build compiles only the
// openai-chat adapter (P13: core + code tools + TUI + ACP). A catalog preset
// that speaks openai-responses and a custom entry with that api must be
// refused at resolution with the explicit full-build message — never sent to
// a missing encoder. The F1 test/eval seam (--base-url) stays on openai-chat,
// which is wired, so it must keep resolving.
func TestProtocolSelection(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	o := &runOptions{apiKey: "sk-test"}
	if _, err := resolveTest(t, &cfg, o, "xai/grok-4"); err == nil ||
		!strings.Contains(err.Error(), "adapter openai-responses tidak tersedia di build minimal; gunakan build penuh") {
		t.Fatalf("xai error = %v, want the explicit minimal-build refusal", err)
	}
	route, err := resolveTest(t, &cfg, &runOptions{apiKey: "sk-test", baseURL: "http://127.0.0.1:9/v1"}, "xai/grok-4")
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	if len(route.Protocols) != 1 || route.Protocols[0] != registry.ProtocolOpenAIChat {
		t.Fatalf("override protocols = %v", route.Protocols)
	}
	cfg.Providers.Custom = []config.CustomProvider{{
		Name: "kantor", BaseURL: "http://127.0.0.1:8000/v1", API: "openai-responses",
		APIKeyEnv: "KANTOR_KEY", Models: []config.CustomModel{{ID: "m"}},
	}}
	if _, err := resolveTest(t, &cfg, &runOptions{}, "kantor/m"); err == nil ||
		!strings.Contains(err.Error(), "adapter openai-responses tidak tersedia di build minimal; gunakan build penuh") {
		t.Fatalf("custom responses error = %v, want the explicit minimal-build refusal", err)
	}
}

// TestProtocolAnthropic (F5-05, minimal): the Messages preset and a custom
// anthropic-messages api have no adapter in the minimal build, so resolution
// must name the missing adapter instead of succeeding.
func TestProtocolAnthropic(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	if _, err := resolveTest(t, &cfg, &runOptions{apiKey: "sk-test"}, "anthropic/claude-3"); err == nil ||
		!strings.Contains(err.Error(), "adapter anthropic tidak tersedia di build minimal; gunakan build penuh") {
		t.Fatalf("anthropic error = %v, want the explicit minimal-build refusal", err)
	}
	cfg.Providers.Custom = []config.CustomProvider{{
		Name: "kantor", BaseURL: "http://127.0.0.1:8000/v1", API: "anthropic-messages",
		APIKeyEnv: "KANTOR_KEY", Models: []config.CustomModel{{ID: "m"}},
	}}
	if _, err := resolveTest(t, &cfg, &runOptions{}, "kantor/m"); err == nil ||
		!strings.Contains(err.Error(), "adapter anthropic tidak tersedia di build minimal; gunakan build penuh") {
		t.Fatalf("custom messages error = %v, want the explicit minimal-build refusal", err)
	}
}

// TestMinimalOpenAIPresetRefusal pins the resolution the full build reaches
// through the openai preset: openai speaks openai-responses, so the minimal
// build refuses it before any credential or key-file check. Tests that only
// exercise later stages (TestEvalKeyFilePermissions) pin openai-chat through
// the --base-url seam for that reason.
func TestMinimalOpenAIPresetRefusal(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	if _, err := resolveTest(t, &cfg, &runOptions{apiKey: "sk-test"}, "openai/gpt-5.4-mini"); err == nil ||
		!strings.Contains(err.Error(), "adapter openai-responses tidak tersedia di build minimal; gunakan build penuh") {
		t.Fatalf("openai error = %v, want the explicit minimal-build refusal", err)
	}
}
