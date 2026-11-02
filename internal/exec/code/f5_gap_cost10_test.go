//go:build !minimal

package code

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/exec"
)

// TestExecuteCodeRPCNetworkDeny is GAP-COST-10's RPC gate: network=deny
// refuses web_fetch and shell before the pipeline, accepts only the seven
// allowlisted tools, and forwards an allowed call to the invoker.
func TestExecuteCodeRPCNetworkDeny(t *testing.T) {
	t.Parallel()
	t.Run("server-policy", func(t *testing.T) { t.Parallel(); testServerPolicy(t) })
	t.Run("starlark-child-argv", func(t *testing.T) { t.Parallel(); testStarlarkArgv(t) })
}

func testServerPolicy(t *testing.T) {
	t.Helper()
	var got string
	r := &Runner{cfg: Config{Tool: toolFunc(func(_ context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
		got = name + ":" + string(args)
		return json.RawMessage(`{"ok":true}`), nil
	})}}
	sock, stop, err := r.startToolServer(context.Background(), t.TempDir(), "deny")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for _, name := range []string{"web_fetch", "shell"} {
		if _, err := CallToolRPC(sock, name, map[string]any{"url": "https://example.test"}); err == nil ||
			!strings.Contains(err.Error(), "network_deny") {
			t.Fatalf("%s = %v, want network_deny", name, err)
		}
	}
	if _, err := CallToolRPC(sock, "process_list", nil); err == nil || !strings.Contains(err.Error(), "invalid_args") {
		t.Fatalf("not-allowlisted = %v", err)
	}
	if _, err := CallToolRPC(sock, "read_file", map[string]any{"path": "x"}); err != nil {
		t.Fatalf("read_file = %v", err)
	}
	if got != `read_file:{"path":"x"}` {
		t.Fatalf("invoker saw %q", got)
	}
}

func testStarlarkArgv(t *testing.T) {
	t.Helper()
	fake := newFake()
	runner, err := New(&Config{Backend: fake, Self: "/tmp/damnbot"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), &Request{Language: Starlark, Code: "print('hi')", Network: "deny"}); err != nil {
		t.Fatal(err)
	}
	spec := fake.lastSpec()
	if len(spec.Argv) < 3 || spec.Argv[1] != "__starlark" || !strings.HasSuffix(spec.Argv[2], "main.star") {
		t.Fatalf("argv = %v", spec.Argv)
	}
	found := false
	for _, kv := range spec.Env {
		found = found || strings.HasPrefix(kv, "DAMNBOT_TOOL_SOCKET=")
	}
	if !found {
		t.Fatalf("env = %v", spec.Env)
	}
}

type toolFunc func(context.Context, string, json.RawMessage) (json.RawMessage, error)

func (f toolFunc) Tool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	return f(ctx, name, args)
}

func (f *fakeBackend) lastSpec() exec.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.specs) == 0 {
		return exec.Spec{}
	}
	return f.specs[len(f.specs)-1]
}

func TestSocketPathFallsBackForLongDir(t *testing.T) {
	t.Parallel()
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 120))
	path, cleanup, err := socketPath(long)
	if err != nil {
		t.Fatal(err)
	}
	if len(path) > 96 || filepath.Base(path) != "t.sock" {
		t.Fatalf("socket path = %q (len %d)", path, len(path))
	}
	cleanup()
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("short socket dir not removed: %v", err)
	}
}
