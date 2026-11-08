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
