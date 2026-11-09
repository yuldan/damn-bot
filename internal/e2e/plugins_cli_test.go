package e2e

import (
	"os"
	"strings"
	"testing"
)

// TestF8PluginsCLI exercises the real binary: the embedded 645-entry catalog
// verifies, lists deterministically, and resolves an MCP target only when the
// config actually registers that server. Nothing is installed or executed.
func TestF8PluginsCLI(t *testing.T) {
	env := newEnv(t)
	verify := env.Exec("plugins", "verify")
	verify.RequireOK(t)
	if verify.Stdout != "ok: 645 entries (mcp 59, skill 30, module 556)\n" {
		t.Fatalf("verify stdout: %q", verify.Stdout)
	}
	list := env.Exec("plugins", "list", "--config", env.cfg)
	list.RequireOK(t)
	lines := strings.Split(strings.TrimSuffix(list.Stdout, "\n"), "\n")
	if len(lines) != 645 || lines[0] != "0bull\tmcp\ttools\tmodule_unavailable" {
		t.Fatalf("list lines = %d, first = %q", len(lines), lines[0])
	}
	show := env.Exec("plugins", "show", "0bull", "--config", env.cfg)
	show.RequireOK(t)
	if !strings.Contains(show.Stdout, "status: module_unavailable\n") ||
		!strings.Contains(show.Stdout, "pin: 240890a71715375bc877b238491e414517e9d902\n") {
		t.Fatalf("show stdout:\n%s", show.Stdout)
	}

	const mcpURL = "https://catalog.invalid/0bull"
	if err := os.WriteFile(env.cfg, []byte("[modules.mcp.servers.0bull]\nurl = \""+mcpURL+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved := env.Exec("plugins", "show", "0bull", "--config", env.cfg)
	resolved.RequireOK(t)
	if !strings.Contains(resolved.Stdout, "target: mcp 0bull\n") ||
		!strings.Contains(resolved.Stdout, "status: available\n") {
		t.Fatalf("resolved show stdout:\n%s", resolved.Stdout)
	}
	if strings.Contains(resolved.Stdout, mcpURL) {
		t.Fatalf("show leaked the config URL:\n%s", resolved.Stdout)
	}
	resolvedList := env.Exec("plugins", "list", "--config", env.cfg)
	resolvedList.RequireOK(t)
	if !strings.Contains(resolvedList.Stdout, "0bull\tmcp\ttools\tavailable\n") {
		t.Fatalf("resolved list misses the target: %s", resolvedList.Stdout)
	}

	if unknown := env.Exec("plugins", "show", "not-a-real-plugin-xyz"); unknown.Code != 2 {
		t.Fatalf("unknown id exit = %d stderr=%s", unknown.Code, unknown.Stderr)
	}
	if bad := env.Exec("plugins", "nope"); bad.Code != 2 {
		t.Fatalf("unknown subcommand exit = %d", bad.Code)
	}
}
