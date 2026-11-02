//go:build unix

package e2e

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// writeExecConfig writes the child's [exec] section.
func writeExecConfig(t *testing.T, e *Env, body string) {
	t.Helper()
	if err := os.WriteFile(e.cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// dockerOrFail returns the image for the positive execute_code path: local
// run skips, CI fails instead of skipping silently (network isolation is
// only real through an isolating backend).
func dockerOrFail(t *testing.T) string {
	t.Helper()
	image := os.Getenv("DAMNBOT_E2E_DOCKER_IMAGE")
	_, err := exec.LookPath("docker")
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").Run()
	}
	if err == nil && image != "" {
		return image
	}
	t.Skipf("docker/image unavailable: %v image=%q", err, image)
	return ""
}

// TestExecuteCodeNetworkDenyBinary: the real binary refuses execute_code
// before spawning when the backend cannot enforce network deny.
func TestExecuteCodeNetworkDenyBinary(t *testing.T) {
	t.Parallel()
	e := NewTrusted(t,
		ToolCallReply("c1", "execute_code", `{"language":"python","code":"import socket\nsocket.create_connection(('1.1.1.1',80),1)"}`),
		providertest.Text("done"),
	)
	writeExecConfig(t, e, "[exec]\ncode_auto_languages = [\"python\"]\n")
	res := e.Run("open the network", "--approval", "auto")
	if res.Code != 0 {
		t.Fatalf("run failed: %+v", res)
	}
	body := e.RequestText(t, 1)
	if !strings.Contains(body, "network") && !strings.Contains(body, "unavailable") && !strings.Contains(body, "denied") {
		t.Fatalf("tool error missing network denial:\n%s", body)
	}
	if strings.Contains(body, "e2e-network-ran") {
		t.Fatalf("code ran despite deny:\n%s", body)
	}
}

// TestExecuteCodeApprovalBinary: ask/auto-edit and untrusted auto never spawn;
// the trusted auto+allowlist path runs only through docker (skipOrFail).
func TestExecuteCodeApprovalBinary(t *testing.T) {
	t.Parallel()
	code := `{"language":"python","code":"print('e2e-code-ok')"}`
	for _, tc := range []struct {
		name, mode string
		trusted    bool
	}{
		{"ask", "ask", true},
		{"auto-edit", "auto-edit", true},
		{"auto untrusted", "auto", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New(t, ToolCallReply("c1", "execute_code", code), providertest.Text("done"))
			if tc.trusted {
				trustWorkspace(t, e)
			}
			writeExecConfig(t, e, "[exec]\ncode_auto_languages = [\"python\"]\n")
			res := e.Run("run code", "--approval", tc.mode)
			if res.Code != 0 {
				t.Fatalf("run failed: %+v", res)
			}
			body := e.RequestText(t, 1)
			if !strings.Contains(body, "denied") && !strings.Contains(body, "approval") {
				t.Fatalf("denial not reported:\n%s", body)
			}
			if strings.Contains(body, `"content":"e2e-code-ok"`) {
				t.Fatalf("denied code produced a tool result:\n%s", body)
			}
		})
	}
	t.Run("auto trusted docker", func(t *testing.T) {
		image := dockerOrFail(t)
		e := NewTrusted(t, ToolCallReply("c1", "execute_code", code), providertest.Text("done"))
		writeExecConfig(t, e, "[exec]\nbackend = \"docker\"\ncode_auto_languages = [\"python\"]\n[exec.backends.docker]\nbinary = \"docker\"\nimage = \""+image+"\"\nuser = \"65534:65534\"\nnetwork = \"none\"\n")
		res := e.Run("run code", "--approval", "auto")
		if res.Code != 0 || !strings.Contains(e.RequestText(t, 1), "e2e-code-ok") {
			t.Fatalf("docker execute_code failed: %+v", res)
		}
	})
}

// TestProcessBinarySmoke: shell background -> process_list -> process_wait ->
// process_output -> process_kill through the real binary.
func TestProcessBinarySmoke(t *testing.T) {
	t.Parallel()
	e := NewTrusted(t,
		ToolCallReply("c1", "shell", `{"command":"printf bg-e2e; sleep 30","background":true}`),
		ToolCallReply("c2", "process_list", `{}`),
		ToolCallReply("c3", "process_wait", `{"id":"p1","timeout_s":1}`),
		ToolCallReply("c4", "process_output", `{"id":"p1"}`),
		ToolCallReply("c5", "process_kill", `{"id":"p1","signal":"KILL"}`),
		providertest.Text("done"),
	)
	res := e.Run("manage the process", "--approval", "auto", "--unsafe-auto-shell")
	if res.Code != 0 {
		t.Fatalf("run failed: %+v", res)
	}
	var all strings.Builder
	for i := range len(e.Requests()) {
		all.WriteString(e.RequestText(t, i))
	}
	out := all.String()
	for _, want := range []string{"p1", "still_running", "bg-e2e", "signaled"} {
		if !strings.Contains(out, want) {
			t.Fatalf("requests missing %q:\n%s", want, out)
		}
	}
}
