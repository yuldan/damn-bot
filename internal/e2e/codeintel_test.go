package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestRepoMapBinary: the real binary exposes repo_map/code_intel read-only,
// wraps untrusted workspace content, lists only Go/C/C++ languages, and
// refuses a path outside the workspace.
func TestRepoMapBinary(t *testing.T) {
	t.Parallel()
	e := New(t,
		ToolCallReply("c1", "repo_map", `{"focus":"Alpha","max_tokens":400}`),
		ToolCallReply("c2", "code_intel", `{"action":"languages"}`),
		ToolCallReply("c3", "repo_map", `{"path":"/etc"}`),
		providertest.Text("done"),
	)
	if err := os.WriteFile(filepath.Join(e.Workdir, "main.go"), []byte("package main\nfunc Alpha() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.Workdir, "extra.js"), []byte("function jsSymbol(){}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.Run("map the repo")
	res.RequireOK(t)
	first := e.RequestText(t, 1)
	if !strings.Contains(first, "func Alpha") || !strings.Contains(first, "untrusted source") {
		t.Fatalf("repo_map result missing symbol/untrusted wrap:\n%s", first[:min(len(first), 1200)])
	}
	if strings.Contains(first, "jsSymbol") {
		t.Fatalf("unsupported language indexed:\n%s", first)
	}
	second := e.RequestText(t, 2)
	if !strings.Contains(second, "go, c, cpp") || strings.Contains(second, "javascript") {
		t.Fatalf("languages = %s", second)
	}
	third := e.RequestText(t, 3)
	if !strings.Contains(third, "outside the workspace") {
		t.Fatalf("outside path not refused:\n%s", third)
	}
}
