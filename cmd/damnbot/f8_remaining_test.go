//go:build !minimal

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func f8ConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestF8CLI012Proxy covers the three proxy subcommands, the typed start
// refusal, and the no-token rule (CLI-012, UI-022).
func TestF8CLI012Proxy(t *testing.T) {
	cfg := f8ConfigFile(t, `[[providers.custom]]
name = "acme"
base_url = "https://api.acme.test/v1"
api = "openai-chat"
api_key_env = "ACME_API_KEY"

[[providers.custom.models]]
id = "acme-1"
`)
	code, out, _ := runF8CLI(t, runProxy, "--config", cfg, "status")
	if code != f8ExitOK || !strings.Contains(out, "proxy stopped") {
		t.Fatalf("status = %d %q", code, out)
	}
	code, out, _ = runF8CLI(t, runProxy, "--config", cfg, "providers")
	if code != f8ExitOK || !strings.Contains(out, "provider acme api=openai-chat models=1") {
		t.Fatalf("providers = %d %q", code, out)
	}
	t.Setenv("ACME_API_KEY", "sk-acme-proxy-secret")
	code, out, errs := runF8CLI(t, runProxy, "--config", cfg, "start")
	if code != f8ExitUnavailable || !strings.Contains(errs, "module_unavailable") {
		t.Fatalf("start = %d %q %q", code, out, errs)
	}
	if strings.Contains(out+errs, "sk-acme-proxy-secret") {
		t.Fatalf("token leaked: %q %q", out, errs)
	}
}

// TestF8CLI012ProxySubcommands checks the closed argument surface.
func TestF8CLI012ProxySubcommands(t *testing.T) {
	if code, _, _ := runF8CLI(t, runProxy); code != f8ExitUsage {
		t.Fatalf("bare = %d", code)
	}
	if code, _, errs := runF8CLI(t, runProxy, "nope"); code != f8ExitUsage || !strings.Contains(errs, "unknown subcommand") {
		t.Fatalf("unknown = %d %q", code, errs)
	}
	if code, _, _ := runF8CLI(t, runProxy, "start", "extra"); code != f8ExitUsage {
		t.Fatalf("start extra = %d", code)
	}
}

// TestF8CLI052Memory covers status and the typed provider refusals
// (CLI-052, MEM-005/008).
func TestF8CLI052Memory(t *testing.T) {
	code, out, _ := runF8CLI(t, runMemory, "status")
	if code != f8ExitOK || !strings.Contains(out, "provider none") || !strings.Contains(out, "active false") {
		t.Fatalf("status = %d %q", code, out)
	}
	for _, sub := range []string{"setup", "off", "reset"} {
		code, out, errs := runF8CLI(t, runMemory, sub)
		if code != f8ExitUnavailable || !strings.Contains(errs, "module_unavailable") {
			t.Fatalf("%s = %d %q %q", sub, code, out, errs)
		}
	}
	if code, _, _ := runF8CLI(t, runMemory); code != f8ExitUsage {
		t.Fatalf("bare = %d", code)
	}
}

// TestF8CLI056Connectors covers list/accounts and the hosted-account
// refusal without printing a token (CLI-056, PLUG-035).
func TestF8CLI056Connectors(t *testing.T) {
	code, out, _ := runF8CLI(t, runConnectors, "list")
	if code != f8ExitOK || !strings.Contains(out, "graph scopes=3 tools=3") || !strings.Contains(out, "feishu") {
		t.Fatalf("list = %d %q", code, out)
	}
	cfg := f8ConfigFile(t, `[modules.connectors]
enabled = true

[modules.connectors.graph]
enabled = true
client_secret_env = "GRAPH_SECRET"
`)
	code, out, _ = runF8CLI(t, runConnectors, "--config", cfg, "accounts")
	if code != f8ExitOK || !strings.Contains(out, "graph enabled=true secret_env=GRAPH_SECRET") {
		t.Fatalf("accounts = %d %q", code, out)
	}
	if strings.Contains(out, "secret-value") {
		t.Fatalf("account token leaked: %q", out)
	}
	for _, sub := range []string{"connect", "rename", "disconnect"} {
		code, _, errs := runF8CLI(t, runConnectors, sub, "graph", "x")
		if code != f8ExitUnavailable || !strings.Contains(errs, "module_unavailable") {
			t.Fatalf("%s = %d %q", sub, code, errs)
		}
	}
	if code, _, _ := runF8CLI(t, runConnectors, "nope"); code != f8ExitUsage {
		t.Fatalf("unknown = %d", code)
	}
}
