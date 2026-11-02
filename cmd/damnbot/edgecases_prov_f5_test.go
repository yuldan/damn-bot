//go:build !minimal

package main

import (
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestLocalAliasGetsV1 (EC-PROV-023): a local catalog alias whose base_url is
// a bare host gets the OpenAI-compatible /v1 root, while an explicit path and
// an explicit --base-url override are left untouched. Without this a local
// Ollama/vLLM endpoint is called at /chat/completions and answers 404.
func TestLocalAliasGetsV1(t *testing.T) {
	g := phase.Current(t)
	if !g.Item(t, "EC-PROV-023", "F5", "covered") {
		return
	}
	t.Setenv("DAMNBOT_E2E_BASE_URL", "")
	cfg := config.Default()
	t.Run("EC-PROV-023", func(t *testing.T) {
		route, err := resolveTest(t, &cfg, nil, "local-ollama/llama3")
		if err != nil {
			t.Fatalf("local-ollama: %v", err)
		}
		if want := "http://127.0.0.1:11434/v1"; route.BaseURL != want || route.Family != want {
			t.Fatalf("base/family = %q/%q, want %q", route.BaseURL, route.Family, want)
		}
		// An alias that already carries a path is not appended twice.
		route, err = resolveTest(t, &cfg, nil, "local-vllm/m")
		if err != nil {
			t.Fatalf("local-vllm: %v", err)
		}
		if want := "http://127.0.0.1:8000/v1"; route.BaseURL != want {
			t.Fatalf("local-vllm base = %q, want %q", route.BaseURL, want)
		}
		// Negative control: an explicit test/eval override is the operator's
		// URL and is never rewritten.
		route, err = resolveTest(t, &cfg, &runOptions{baseURL: "http://127.0.0.1:11434"}, "local-ollama/llama3")
		if err != nil {
			t.Fatalf("override: %v", err)
		}
		if route.BaseURL != "http://127.0.0.1:11434" {
			t.Fatalf("override base = %q, want it unchanged", route.BaseURL)
		}
	})
}
