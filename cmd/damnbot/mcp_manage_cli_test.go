//go:build !minimal

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func mcpConfigPath(t *testing.T, home string) string {
	t.Helper()
	path := filepath.Join(home, "damnbot", "config.toml")
	if runtime.GOOS == "darwin" {
		path = filepath.Join(home, "Library", "Application Support", "damnbot", "config.toml")
	}
	return path
}

func TestMCPAddListRemove(t *testing.T) { //nolint:gocyclo // one CLI round trip
	mcpTestHome(t)
	home := os.Getenv("HOME")
	code, out, errOut := runMCPArgs(t, "add", "demo", "--url", "https://mcp.example.test/v1", "--trust", "full", "--timeout", "30", "--read-only", "search")
	if code != 0 {
		t.Fatalf("add = %d, out=%q err=%q", code, out, errOut)
	}
	configPath := mcpConfigPath(t, home)
	body, err := os.ReadFile(configPath) // #nosec G304 -- test fixture
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, want := range []string{"[modules.mcp.servers.demo]", `url = "https://mcp.example.test/v1"`, "trust = \"full\"", "timeout_s = 30", "[modules.mcp.servers.demo.tools.\"search\"]"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("config missing %q:\n%s", want, body)
		}
	}
	if fi, serr := os.Stat(configPath); serr != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("config mode = %v, %v (want 0600)", fi, serr)
	}
	if dup, _, _ := runMCPArgs(t, "add", "demo", "--url", "https://other.example.test/v1"); dup != 1 {
		t.Fatalf("duplicate add = %d, want 1", dup)
	}
	code, out, errOut = runMCPArgs(t, "list", "--json")
	if code != 0 {
		t.Fatalf("list = %d, err=%q", code, errOut)
	}
	var rows []map[string]any
	if jerr := json.Unmarshal([]byte(out), &rows); jerr != nil || len(rows) != 1 || rows[0]["name"] != "demo" || rows[0]["transport"] != "http" {
		t.Fatalf("list json = %q (%v)", out, jerr)
	}
	code, out, errOut = runMCPArgs(t, "remove", "demo")
	if code != 0 || !strings.Contains(out, "removed mcp server demo") {
		t.Fatalf("remove = %d out=%q err=%q", code, out, errOut)
	}
	body, err = os.ReadFile(configPath) // #nosec G304 -- test fixture
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "modules.mcp.servers.demo") {
		t.Fatalf("config still has demo:\n%s", body)
	}
	if gone, _, _ := runMCPArgs(t, "remove", "demo"); gone != 1 {
		t.Fatalf("remove missing = %d, want 1", gone)
	}
}

func TestMCPAddValidation(t *testing.T) {
	mcpTestHome(t)
	cases := [][]string{
		{"add", "Bad_Name", "--url", "https://h/mcp"},
		{"add", "demo"},
		{"add", "demo", "--url", "https://h/mcp", "--command", "srv"},
		{"add", "demo", "--url", "https://h/mcp", "--timeout", "0"},
		{"add", "demo", "--url", "https://h/mcp", "--client-secret", "plaintext"},
		{"add", "demo", "--command", "srv", "--issuer", "https://as.example"},
		{"nope"},
	}
	for _, args := range cases {
		if code, _, _ := runMCPArgs(t, args...); code != 2 {
			t.Errorf("%q = %d, want 2", args, code)
		}
	}
}

func TestMCPAddOAuthSection(t *testing.T) {
	mcpTestHome(t)
	home := os.Getenv("HOME")
	code, _, errOut := runMCPArgs(t, "add", "remote", "--url", "http://127.0.0.1:8080/mcp",
		"--issuer", "http://localhost:9090", "--client-id", "dcr-1", "--scope", "mcp.read", "--scope", "mcp.tools")
	if code != 0 {
		t.Fatalf("add oauth = %d, err=%q", code, errOut)
	}
	body, err := os.ReadFile(mcpConfigPath(t, home)) // #nosec G304 -- test fixture
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "[modules.mcp.servers.remote.oauth]") ||
		!strings.Contains(string(body), `scopes = ["mcp.read", "mcp.tools"]`) {
		t.Fatalf("oauth section missing:\n%s", body)
	}
	if strings.Contains(string(body), "access_token") || strings.Contains(string(body), "refresh_token") {
		t.Fatalf("config holds a token:\n%s", body)
	}
}

// TestMCPRegistryReload: a config edit is picked up atomically; a rejected
// config leaves the previous server set in place.
func TestMCPRegistryReload(t *testing.T) {
	mcpTestHome(t)
	home := os.Getenv("HOME")
	configPath := mcpConfigPath(t, home)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "[modules.mcp.servers.one]\nurl = \"https://mcp.example.test/v1\"\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil { // #nosec G306 -- test fixture
		t.Fatal(err)
	}
	reg, err := newMCPRegistry(configPath)
	if err != nil {
		t.Fatalf("newMCPRegistry: %v", err)
	}
	if got := reg.Servers(); len(got) != 1 || got[0].Name != "one" {
		t.Fatalf("servers = %+v", got)
	}
	body += "\n[modules.mcp.servers.two]\nurl = \"https://mcp.example.test/v2\"\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil { // #nosec G306 -- test fixture
		t.Fatal(err)
	}
	n, rerr := reg.ReloadMCP(t.Context())
	if rerr != nil || n != 2 {
		t.Fatalf("ReloadMCP = %d, %v", n, rerr)
	}
	if got := reg.Servers(); len(got) != 2 || got[1].Name != "two" {
		t.Fatalf("servers after reload = %+v", got)
	}
	if err := os.WriteFile(configPath, []byte("[modules.mcp.servers.bad]\ncommand = [\"x\"]\nurl = \"https://h/mcp\"\n"), 0o600); err != nil { // #nosec G306 -- test fixture
		t.Fatal(err)
	}
	if _, err := reg.ReloadMCP(t.Context()); err == nil {
		t.Fatal("invalid config reloaded")
	}
	if got := reg.Servers(); len(got) != 2 {
		t.Fatalf("failed reload changed servers: %+v", got)
	}
}
