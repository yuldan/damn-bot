package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// mcpTestHome isolates config and data for one MCP CLI test (not parallel:
// the profile paths come from process environment).
func mcpTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("APPDATA", home)
	t.Setenv("LOCALAPPDATA", home)
	t.Setenv("HOME", home)
	return home
}

func runMCPArgs(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code := runMCP(args, &out, &errBuf)
	return code, out.String(), errBuf.String()
}

func TestMCPAddListRemove(t *testing.T) {
	home := mcpTestHome(t)
	code, out, errOut := runMCPArgs(t, "add", "demo", "--url", "https://mcp.example.test/v1", "--trust", "full", "--timeout", "30", "--read-only", "search")
	if code != 0 {
		t.Fatalf("add = %d, out=%q err=%q", code, out, errOut)
	}
	configPath := filepath.Join(home, "damnbot", "config.toml")
	if runtime.GOOS == "darwin" {
		configPath = filepath.Join(home, "Library", "Application Support", "damnbot", "config.toml")
	}
	body, err := os.ReadFile(configPath) // #nosec G304 -- test fixture
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, want := range []string{"[modules.mcp.servers.demo]", `url = "https://mcp.example.test/v1"`, "trust = \"full\"", "timeout_s = 30", "[modules.mcp.servers.demo.tools.\"search\"]"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("config missing %q:\n%s", want, body)
		}
	}
	if fi, err := os.Stat(configPath); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("config mode = %v, %v (want 0600)", fi, err)
	}
	if code, _, _ := runMCPArgs(t, "add", "demo", "--url", "https://other.example.test/v1"); code != 1 {
		t.Fatalf("duplicate add = %d, want 1", code)
	}
	code, out, errOut = runMCPArgs(t, "list", "--json")
	if code != 0 {
		t.Fatalf("list = %d, err=%q", code, errOut)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 || rows[0]["name"] != "demo" || rows[0]["transport"] != "http" {
		t.Fatalf("list json = %q (%v)", out, err)
	}
	code, out, errOut = runMCPArgs(t, "remove", "demo")
	if code != 0 || !strings.Contains(out, "removed mcp server demo") {
		t.Fatalf("remove = %d out=%q err=%q", code, out, errOut)
	}
	if body, _ := os.ReadFile(configPath); strings.Contains(string(body), "modules.mcp.servers.demo") { // #nosec G304 -- test fixture
		t.Fatalf("config still has demo:\n%s", body)
	}
	if code, _, _ := runMCPArgs(t, "remove", "demo"); code != 1 {
		t.Fatalf("remove missing = %d, want 1", code)
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
	home := mcpTestHome(t)
	code, _, errOut := runMCPArgs(t, "add", "remote", "--url", "http://127.0.0.1:8080/mcp",
		"--issuer", "http://localhost:9090", "--client-id", "dcr-1", "--scope", "mcp.read", "--scope", "mcp.tools")
	if code != 0 {
		t.Fatalf("add oauth = %d, err=%q", code, errOut)
	}
	configPath := filepath.Join(home, "damnbot", "config.toml")
	if runtime.GOOS == "darwin" {
		configPath = filepath.Join(home, "Library", "Application Support", "damnbot", "config.toml")
	}
	body, err := os.ReadFile(configPath) // #nosec G304 -- test fixture
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

func TestMCPRegistryReload(t *testing.T) {
	home := mcpTestHome(t)
	configPath := filepath.Join(home, "damnbot", "config.toml")
	if runtime.GOOS == "darwin" {
		configPath = filepath.Join(home, "Library", "Application Support", "damnbot", "config.toml")
	}
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
	n, err := reg.ReloadMCP(t.Context())
	if err != nil || n != 2 {
		t.Fatalf("ReloadMCP = %d, %v", n, err)
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
