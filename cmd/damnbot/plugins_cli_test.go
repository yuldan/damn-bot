//go:build !minimal

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestF8PluginsCLI proves the Q18 surface: verify reports the closed corpus,
// list is ordered and stable, show prints the pinned row, a configured MCP
// server resolves the entry, and usage errors exit 2.
func TestF8PluginsCLI(t *testing.T) {
	t.Parallel()
	pluginsVerifyCase(t)
	list := pluginsListCase(t)
	pluginsShowCase(t)
	pluginsResolvedCase(t, list)
	pluginsUsageCase(t)
}

func pluginsVerifyCase(t *testing.T) {
	t.Helper()
	code, out, errOut := runArgs(t, "plugins", "verify")
	if code != 0 || out != "ok: 645 entries (mcp 59, skill 30, module 556)\n" {
		t.Fatalf("plugins verify = (%d, %q, %q)", code, out, errOut)
	}
}

func pluginsListCase(t *testing.T) string {
	t.Helper()
	code, out, errOut := runArgs(t, "plugins", "list")
	if code != 0 {
		t.Fatalf("plugins list = (%d, %q)", code, errOut)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 645 {
		t.Fatalf("plugins list lines = %d, want 645", len(lines))
	}
	if lines[0] != "0bull\tmcp\ttools\tmodule_unavailable" {
		t.Fatalf("first line = %q", lines[0])
	}
	if !sortedIDs(lines) {
		t.Fatal("plugins list is not sorted by id")
	}
	code, again, _ := runArgs(t, "plugins", "list")
	if code != 0 || again != out {
		t.Fatal("plugins list is not stable across runs")
	}
	return out
}

func pluginsShowCase(t *testing.T) {
	t.Helper()
	code, out, _ := runArgs(t, "plugins", "show", "0bull")
	if code != 0 {
		t.Fatalf("plugins show exit = %d", code)
	}
	for _, want := range []string{
		"id: 0bull\n", "kind: mcp\n", "category: tools\n",
		"pin: 240890a71715375bc877b238491e414517e9d902\n",
		"status: module_unavailable\n", "description: Control real iPhones",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("show output misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "https://") || strings.Contains(out, "http://") {
		t.Errorf("show output leaked a URL:\n%s", out)
	}
}

func pluginsResolvedCase(t *testing.T, list string) {
	t.Helper()
	const mcpURL = "https://catalog.invalid/0bull"
	cfg := filepath.Join(t.TempDir(), "config.toml")
	body := "[modules.mcp.servers.0bull]\nurl = \"" + mcpURL + "\"\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runArgs(t, "plugins", "show", "--config", cfg, "0bull")
	if code != 0 {
		t.Fatalf("plugins show --config = (%d, %q)", code, errOut)
	}
	for _, want := range []string{"target: mcp 0bull\n", "status: available\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("resolved show misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, mcpURL) {
		t.Errorf("resolved show leaked the config URL:\n%s", out)
	}
	code, out, _ = runArgs(t, "plugins", "list", "--config", cfg)
	if code != 0 || !strings.Contains(out, "0bull\tmcp\ttools\tavailable\n") {
		t.Fatalf("resolved list = (%d)\n%s", code, out)
	}
	if out == list {
		t.Fatal("resolved list did not change the status")
	}
}

func pluginsUsageCase(t *testing.T) {
	t.Helper()
	for _, args := range [][]string{
		{"plugins"},
		{"plugins", "nope"},
		{"plugins", "show"},
		{"plugins", "show", "not-a-real-plugin-xyz"},
		{"plugins", "show", "0bull", "extra"},
		{"plugins", "verify", "extra"},
		{"plugins", "list", "extra"},
	} {
		if code, _, _ := runArgs(t, args...); code != 2 {
			t.Errorf("%q exit = %d, want 2", args, code)
		}
	}
}

// sortedIDs reports whether the first tab-separated column is ascending.
func sortedIDs(lines []string) bool {
	for i := 1; i < len(lines); i++ {
		prev, _, _ := strings.Cut(lines[i-1], "\t")
		cur, _, _ := strings.Cut(lines[i], "\t")
		if prev >= cur {
			return false
		}
	}
	return true
}
