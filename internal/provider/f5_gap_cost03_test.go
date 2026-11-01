package provider

import (
	"bytes"
	"testing"
)

// TestGAPF5_COST03 covers the full origin matrix of the adaptive Anthropic
// cache TTL and the catalog clamp: interactive origins ask for 1h only when
// the route claims it, every batch origin keeps the 5m default.
func TestGAPF5_COST03(t *testing.T) {
	t.Parallel()
	rt := anthropicRoute()
	rt.CacheTTLMax = 3600
	interactive := []string{"tui", "acp", "web", "gateway"}
	batch := []string{"cron", "batch", "delegate", "kanban", "headless"}
	for _, origin := range interactive {
		if cc := CacheControl(rt, origin); cc["ttl"] != "1h" {
			t.Errorf("%s: cache_control = %v, want 1h", origin, cc)
		}
	}
	for _, origin := range batch {
		if cc := CacheControl(rt, origin); cc["ttl"] != "" {
			t.Errorf("%s: cache_control = %v, want 5m default", origin, cc)
		}
	}
	rt.CacheTTLMax = 300
	for _, origin := range interactive {
		if cc := CacheControl(rt, origin); cc["ttl"] != "" {
			t.Errorf("%s with cache_ttl_max=300: %v, want clamped 5m", origin, cc)
		}
	}
	rt.CacheTTLMax = 0
	if cc := CacheControl(rt, "tui"); cc["ttl"] != "" {
		t.Errorf("no catalog claim: %v, want 5m", cc)
	}

	rt.CacheTTLMax = 3600
	req := boundaryRequest(1)
	req.Origin = "tui"
	body, err := encode(rt, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"ttl":"1h"`)) {
		t.Fatalf("interactive body lacks the 1h breakpoint:\n%s", body)
	}
	req.Origin = "cron"
	body, err = encode(rt, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(`"ttl"`)) {
		t.Fatalf("batch body carries an explicit ttl:\n%s", body)
	}
}
