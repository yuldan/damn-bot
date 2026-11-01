//go:build !minimal

package config

import (
	"errors"
	"testing"
)

// TestMCPConfigStrict binds [modules.mcp] to the fail-closed schema
// (ADR-269): untrusted and 120 s by default, keychain-only headers, and one
// transport per server.
func TestMCPConfigStrict(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, `
[modules.mcp]
allow_sampling = true

[modules.mcp.servers.github]
command = ["github-mcp-server", "stdio"]
env = { GITHUB_TOKEN = "keychain:github" }

[modules.mcp.servers.remote]
url = "https://mcp.example.test/v1/mcp?x=1"
headers = { Authorization = "keychain:remote" }
trust = "full"
timeout_s = 30

[modules.mcp.servers.remote.tools.search]
read_only = true
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg.Modules.MCP.AllowSampling {
		t.Error("allow_sampling = false")
	}
	gh := cfg.Modules.MCP.Servers["github"]
	if gh.Trust != "untrusted" || gh.TimeoutS != 120 {
		t.Errorf("github defaults = %q/%d, want untrusted/120", gh.Trust, gh.TimeoutS)
	}
	remote := cfg.Modules.MCP.Servers["remote"]
	if remote.Trust != "full" || remote.TimeoutS != 30 {
		t.Errorf("remote = %q/%d", remote.Trust, remote.TimeoutS)
	}
	if !remote.Tools["search"].ReadOnly {
		t.Error("remote.tools.search.read_only = false")
	}
}

func TestMCPOAuthConfig(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, `
[modules.mcp.servers.remote]
url = "https://mcp.example.test/v1/mcp"

[modules.mcp.servers.remote.oauth]
issuer = "https://as.example.test"
client_id = "dcr-1"
scopes = ["mcp.read", "mcp.tools"]
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	oauth := cfg.Modules.MCP.Servers["remote"].OAuth
	if oauth == nil || oauth.Issuer != "https://as.example.test" || len(oauth.Scopes) != 2 {
		t.Fatalf("oauth = %+v", oauth)
	}
	if _, err := parse(t, `
[modules.mcp.servers.remote]
url = "http://127.0.0.1:8080/mcp"
[modules.mcp.servers.remote.oauth]
issuer = "http://localhost:9090"
scopes = ["mcp.read"]
`); err != nil {
		t.Fatalf("loopback oauth rejected: %v", err)
	}
}

func TestMCPOAuthRejects(t *testing.T) {
	t.Parallel()
	base := "[modules.mcp.servers.s]\nurl = \"https://h/mcp\"\n"
	tests := []struct{ name, body string }{
		{"stdio", "[modules.mcp.servers.s]\ncommand = [\"x\"]\n[modules.mcp.servers.s.oauth]\nissuer = \"https://as\"\nscopes = [\"r\"]\n"},
		{"plaintext_issuer", base + "[modules.mcp.servers.s.oauth]\nissuer = \"http://as.example\"\nscopes = [\"r\"]\n"},
		{"query_issuer", base + "[modules.mcp.servers.s.oauth]\nissuer = \"https://as.example/?x=1\"\nscopes = [\"r\"]\n"},
		{"no_scopes", base + "[modules.mcp.servers.s.oauth]\nissuer = \"https://as.example\"\n"},
		{"wildcard", base + "[modules.mcp.servers.s.oauth]\nissuer = \"https://as.example\"\nscopes = [\"*\"]\n"},
		{"duplicate", base + "[modules.mcp.servers.s.oauth]\nissuer = \"https://as.example\"\nscopes = [\"r\", \"r\"]\n"},
		{"plaintext_secret", base + "[modules.mcp.servers.s.oauth]\nissuer = \"https://as.example\"\nclient_secret = \"s3cret\"\nscopes = [\"r\"]\n"},
		{"header_conflict", "[modules.mcp.servers.s]\nurl = \"https://h/mcp\"\nheaders = { Authorization = \"keychain:t\" }\n[modules.mcp.servers.s.oauth]\nissuer = \"https://as.example\"\nscopes = [\"r\"]\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parse(t, tc.body); err == nil {
				t.Fatal("parse succeeded, want error")
			}
		})
	}
}

func TestMCPConfigRejects(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, body string }{
		{"both_transports", "[modules.mcp.servers.s]\ncommand = [\"x\"]\nurl = \"https://h/mcp\"\n"},
		{"no_transport", "[modules.mcp.servers.s]\ntimeout_s = 10\n"},
		{"bad_trust", "[modules.mcp.servers.s]\ncommand = [\"x\"]\ntrust = \"yes\"\n"},
		{"long_timeout", "[modules.mcp.servers.s]\ncommand = [\"x\"]\ntimeout_s = 601\n"},
		{"plaintext_header", "[modules.mcp.servers.s]\nurl = \"https://h/mcp\"\nheaders = { Authorization = \"Bearer x\" }\n"},
		{"reserved_header", "[modules.mcp.servers.s]\nurl = \"https://h/mcp\"\nheaders = { \"Mcp-Session-Id\" = \"keychain:x\" }\n"},
		{"url_userinfo", "[modules.mcp.servers.s]\nurl = \"https://u:p@h/mcp\"\n"},
		{"bad_name", "[modules.mcp.servers.S]\ncommand = [\"x\"]\n"},
		{"empty_argv", "[modules.mcp.servers.s]\ncommand = [\"x\", \"\"]\n"},
		{"bad_env", "[modules.mcp.servers.s]\ncommand = [\"x\"]\nenv = { \"1BAD\" = \"v\" }\n"},
		{"bad_env_ref", "[modules.mcp.servers.s]\ncommand = [\"x\"]\nenv = { TOKEN = \"keychain:\" }\n"},
		{"unknown_key", "[modules.mcp.servers.s]\ncommand = [\"x\"]\nbogus = 1\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parse(t, tc.body); err == nil {
				t.Fatal("parse succeeded, want error")
			} else if !errors.Is(err, ErrInvalidValue) && !errors.Is(err, ErrModuleUnavailable) && !errors.Is(err, ErrUnknownKey) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
