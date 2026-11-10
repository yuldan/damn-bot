//go:build !minimal

package config

import "testing"

// TestF8ConnectorsSection covers the C1 core of [modules.connectors]: the
// default-off zero value, valid limits, and each fail-closed range.
func TestF8ConnectorsSection(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, "")
	if err != nil {
		t.Fatalf("empty config: %v", err)
	}
	if cfg.Modules.Connectors.Enabled {
		t.Fatal("connectors are enabled by default")
	}
	cfg, err = parse(t, "[modules.connectors]\nenabled = true\ntimeout_s = 15\nmax_retries = 2\nmax_response_bytes = 1048576\n")
	if err != nil {
		t.Fatalf("valid section: %v", err)
	}
	if !cfg.Modules.Connectors.Enabled || cfg.Modules.Connectors.TimeoutS != 15 {
		t.Fatalf("decoded = %+v", cfg.Modules.Connectors)
	}
	for _, bad := range []string{
		"[modules.connectors]\ntimeout_s = 121\n",
		"[modules.connectors]\nmax_retries = 4\n",
		"[modules.connectors]\nmax_response_bytes = 8388609\n",
		"[modules.connectors]\nunknown_key = true\n",
	} {
		if _, err := parse(t, bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// TestF8MemorySection: [modules.memory] is default off, accepts the Honcho
// opt-in with a keychain reference, and rejects every unwired combination.
func TestF8MemorySection(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, "")
	if err != nil {
		t.Fatalf("empty config: %v", err)
	}
	mem := cfg.Modules.Memory
	if mem.Provider != "" || mem.Honcho.Enabled || mem.QueryRewrite || mem.ContextEngine != "" {
		t.Fatalf("memory defaults = %+v", mem)
	}
	valid := "[modules.memory]\nprovider = \"honcho\"\nquery_rewrite = true\ncontext_engine = \"compressor\"\n" +
		"[modules.memory.honcho]\nenabled = true\ntoken_secret = \"honcho_api_key\"\n" +
		"base_url = \"https://api.honcho.dev\"\nworkspace = \"ws\"\n"
	cfg, err = parse(t, valid)
	if err != nil {
		t.Fatalf("valid memory section: %v", err)
	}
	if !cfg.Modules.Memory.Honcho.Enabled || cfg.Modules.Memory.Honcho.TokenSecret != "honcho_api_key" {
		t.Fatalf("decoded = %+v", cfg.Modules.Memory)
	}
	for _, bad := range []string{
		"[modules.memory]\nprovider = \"byterover\"\n",
		"[modules.memory]\nprovider = \"retaindb\"\n",
		"[modules.memory]\nprovider = \"honcho\"\n",
		"[modules.memory]\nprovider = \"honcho\"\n[modules.memory.honcho]\nenabled = true\n",
		"[modules.memory]\nprovider = \"other\"\n",
		"[modules.memory]\ncontext_engine = \"other\"\n",
		"[modules.memory]\nprovider = \"honcho\"\n[modules.memory.honcho]\nenabled = true\ntoken_secret = \"k\"\nbase_url = \"http://example.com\"\n",
	} {
		if _, err := parse(t, bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
