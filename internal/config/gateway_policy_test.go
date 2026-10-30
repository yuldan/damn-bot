package config

import (
	"errors"
	"testing"
)

// TestGatewayBusyPolicyAndThreadFallback binds the two residual keys to their
// explicit values: busy_policy (queue|steer|reject_notify) and
// modules.gateway.telegram.thread_fallback (fail|parent), with fail-closed
// defaults and no silent acceptance of other [modules] keys.
func TestGatewayBusyPolicyAndThreadFallback(t *testing.T) {
	cfg, err := parse(t, "[gateway]\nbusy_policy = \"steer\"\n[modules.gateway.telegram]\nthread_fallback = \"parent\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gateway.BusyPolicy != "steer" || cfg.Modules.Gateway.Telegram.ThreadFallback != "parent" {
		t.Fatalf("parsed = %q/%q", cfg.Gateway.BusyPolicy, cfg.Modules.Gateway.Telegram.ThreadFallback)
	}
	def := Default()
	if def.Gateway.BusyPolicy != "queue" || def.Modules.Gateway.Telegram.ThreadFallback != "fail" {
		t.Fatalf("defaults = %q/%q", def.Gateway.BusyPolicy, def.Modules.Gateway.Telegram.ThreadFallback)
	}
	for _, bad := range []struct{ key, body string }{
		{"gateway.busy_policy", "[gateway]\nbusy_policy = \"bogus\"\n"},
		{"modules.gateway.telegram.thread_fallback", "[modules.gateway.telegram]\nthread_fallback = \"bogus\"\n"},
	} {
		if _, err := parse(t, bad.body); !errors.Is(err, ErrInvalidValue) {
			t.Errorf("%s = %v, want ErrInvalidValue", bad.key, err)
		}
	}
	if _, err := parse(t, "[modules.mcp]\nfoo = 1\n"); !errors.Is(err, ErrModuleUnavailable) {
		t.Errorf("other module key = %v, want ErrModuleUnavailable", err)
	}
}
