package config

import (
	"errors"
	"testing"
)

// TestProvidersCustom (COST-02): [[providers.custom]] exposes the model
// context-length override; other custom-provider fields remain unknown
// until their phase.
func TestProvidersCustom(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, `[[providers.custom]]
name = "kantor"
base_url = "http://10.0.0.5:8000/v1"
models = [{ id = "qwen3-coder", context_length = 131072, max_tokens = 8192, capabilities = ["tools"] }]
`)
	if err != nil {
		t.Fatalf("valid custom provider rejected: %v", err)
	}
	if len(cfg.Providers.Custom) != 1 {
		t.Fatalf("custom providers = %+v", cfg.Providers.Custom)
	}
	p := cfg.Providers.Custom[0]
	if p.Name != "kantor" || p.BaseURL != "http://10.0.0.5:8000/v1" || len(p.Models) != 1 ||
		p.Models[0].ID != "qwen3-coder" || p.Models[0].ContextLength != 131072 {
		t.Fatalf("custom provider = %+v", p)
	}
	clone := cfg.Clone()
	clone.Providers.Custom[0].Models[0].Capabilities[0] = "mutated"
	if cfg.Providers.Custom[0].Models[0].Capabilities[0] != "tools" {
		t.Error("Clone shares the capabilities slice")
	}

	cases := []struct {
		name, toml string
		kind       error
	}{
		{"missing models", `[[providers.custom]]
name = "kantor"
base_url = "http://x/v1"
`, ErrInvalidValue},
		{"window below minimum", `[[providers.custom]]
name = "kantor"
base_url = "http://x/v1"
models = [{ id = "m", context_length = 4096 }]
`, ErrInvalidValue},
		{"duplicate name", `[[providers.custom]]
name = "kantor"
base_url = "http://x/v1"
models = [{ id = "m", context_length = 8192 }]
[[providers.custom]]
name = "kantor"
base_url = "http://y/v1"
models = [{ id = "n", context_length = 8192 }]
`, ErrInvalidValue},
		{"unknown custom field", `[[providers.custom]]
name = "kantor"
base_url = "http://x/v1"
api = "openai-chat"
models = [{ id = "m", context_length = 8192 }]
`, ErrUnknownKey},
	}
	for _, c := range cases {
		_, err := parse(t, c.toml)
		if ke := keyErrorOf(t, err, c.kind); ke.Key == "" {
			t.Errorf("%s: empty key", c.name)
		}
	}
	if _, err := parse(t, "[[providers.custom]]\nname = \"x\"\n"); !errors.Is(err, ErrInvalidValue) {
		t.Errorf("incomplete provider: %v", err)
	}
}
