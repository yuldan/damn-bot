package e2e

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// decodedRequest decodes the i-th provider request and joins every string in
// it, so assertions read the text the model sees: in the raw body the wrapper
// marker and every newline are JSON-escaped, which would make a literal
// search for <untrusted source="workspace"> silently vacuous.
func decodedRequest(t *testing.T, e *Env, i int) string {
	t.Helper()
	reqs := e.Requests()
	if i >= len(reqs) {
		t.Fatalf("request %d missing (%d recorded)", i, len(reqs))
	}
	var v any
	if err := json.Unmarshal(reqs[i], &v); err != nil {
		t.Fatalf("request %d is not JSON: %v", i, err)
	}
	var b strings.Builder
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			b.WriteString(t)
			b.WriteString("\n")
		case []any:
			for _, y := range t {
				walk(y)
			}
		case map[string]any:
			for _, y := range t {
				walk(y)
			}
		}
	}
	walk(v)
	return b.String()
}

// workspaceWrapped returns the body of every <untrusted source="workspace">
// block in s (the same marker the assembler writes for an untrusted context
// file tier).
func workspaceWrapped(s string) []string {
	const open, close = `<untrusted source="workspace">`, "</untrusted>"
	var out []string
	for {
		i := strings.Index(s, open)
		if i < 0 {
			return out
		}
		s = s[i+len(open):]
		j := strings.Index(s, close)
		if j < 0 {
			return out
		}
		out = append(out, s[:j])
		s = s[j+len(close):]
	}
}

// wrappedContains reports whether needle appears inside any workspace block.
func wrappedContains(blocks []string, needle string) bool {
	for _, b := range blocks {
		if strings.Contains(b, needle) {
			return true
		}
	}
	return false
}

// writeMarkerShellArgs renders a shell call that writes marker into path, the
// command shape the untrusted-workspace scenario refuses.
func writeMarkerShellArgs(t *testing.T, path, marker string) string {
	t.Helper()
	cmd := "sh -c 'echo " + marker + " > \"" + path + "\"'"
	if runtime.GOOS == "windows" {
		cmd = `cmd /c "echo ` + marker + ` > \"` + path + `\""`
	}
	raw, err := json.Marshal(struct {
		Command string `json:"command"`
	}{Command: cmd})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestE2EContextFileWrappedUntrusted (MEM-026, SPEC §4.8/§14; F2-ACCEPTANCE
// backlog item 11(d)): the workspace context file tier is filled in
// production and its <untrusted source="workspace"> wrapper follows the
// workspace trust decision, observed through the real binary.
func TestE2EContextFileWrappedUntrusted(t *testing.T) {
	t.Parallel()
	const (
		fileMarker = "AGENTS-MARKER-3f1c9a"
		userMarker = "USER-MARKER-b27d40"
	)

	// B1: the marker in the workspace AGENTS.md reaches the model inside the
	// workspace wrapper, and nowhere else in the request.
	t.Run("B1 untrusted file is wrapped", func(t *testing.T) {
		t.Parallel()
		e := New(t, providertest.Text("done"))
		e.WriteFile("AGENTS.md", "# agents\nrule: "+fileMarker+"\n")
		res := e.Run("say hi")
		res.RequireOK(t)
		req := decodedRequest(t, e, 0)
		blocks := workspaceWrapped(req)
		if !wrappedContains(blocks, fileMarker) {
			t.Fatalf("AGENTS.md marker is not inside <untrusted source=\"workspace\">:\n%s", req)
		}
		if got, want := strings.Count(req, fileMarker), strings.Count(strings.Join(blocks, ""), fileMarker); got != want {
			t.Fatalf("marker appears %d times, %d inside wrappers (it leaked outside)", got, want)
		}
	})

	// B2 control: text typed by the user is not wrapped; the wrapper is the
	// file tier's, not a blanket wrapper of the request.
	t.Run("B2 user text is not wrapped", func(t *testing.T) {
		t.Parallel()
		e := New(t, providertest.Text("done"))
		e.WriteFile("AGENTS.md", "# agents\nrule: "+fileMarker+"\n")
		res := e.Run("remember this: " + userMarker)
		res.RequireOK(t)
		req := decodedRequest(t, e, 0)
		if !strings.Contains(req, userMarker) {
			t.Fatalf("user text missing from the request:\n%s", req)
		}
		if wrappedContains(workspaceWrapped(req), userMarker) {
			t.Fatalf("user text was wrapped as workspace data:\n%s", req)
		}
	})

	// B3 two-way control: with the same workspace registered in the trust
	// store the identical file is *not* wrapped, so the wrapper is controlled
	// by the trust decision and not by the presence of a file.
	t.Run("B3 trusted file is not wrapped", func(t *testing.T) {
		t.Parallel()
		e := New(t, providertest.Text("done"))
		e.WriteFile("AGENTS.md", "# agents\nrule: "+fileMarker+"\n")
		trustWorkspace(t, e)
		res := e.Run("say hi")
		res.RequireOK(t)
		req := decodedRequest(t, e, 0)
		if !strings.Contains(req, fileMarker) {
			t.Fatalf("trusted AGENTS.md missing from the request:\n%s", req)
		}
		if wrappedContains(workspaceWrapped(req), fileMarker) {
			t.Fatalf("trusted workspace file was wrapped:\n%s", req)
		}
	})

	// B4: no context file, no wrapper (a weak control on its own).
	t.Run("B4 no file no wrapper", func(t *testing.T) {
		t.Parallel()
		e := New(t, providertest.Text("done"))
		res := e.Run("say hi")
		res.RequireOK(t)
		if blocks := workspaceWrapped(decodedRequest(t, e, 0)); len(blocks) != 0 {
			t.Fatalf("wrapper without a context file: %q", blocks)
		}
	})

	// The trust decision also gates the shell auto paths (SPEC §14/§10): in an
	// untrusted workspace --unsafe-auto-shell and shell_allow are dead, and the
	// same command runs once the workspace is registered.
	t.Run("B5 untrusted workspace disables the shell auto paths", func(t *testing.T) {
		t.Parallel()
		e := New(t)
		target := filepath.Join(e.Workdir, "shell-side-effect.txt")
		args := writeMarkerShellArgs(t, target, "SHELL-UNTRUSTED-9a3e")
		e.Server.Push(ToolCallReply("c1", "shell", args), providertest.Text("done"))
		res := e.Run("write the file", "--approval", "auto", "--unsafe-auto-shell", "--output", "stream-json")
		if res.Code != 0 {
			t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
		}
		if _, err := os.Stat(target); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("untrusted workspace ran the command: %v", err)
		}
		if !strings.Contains(res.Stdout, "shell is not allowed in this mode") {
			t.Fatalf("untrusted workspace did not refuse the shell call:\n%s", res.Stdout)
		}
	})

	t.Run("B6 trusted workspace runs the same command", func(t *testing.T) {
		t.Parallel()
		e := New(t)
		trustWorkspace(t, e)
		target := filepath.Join(e.Workdir, "shell-side-effect.txt")
		args := writeMarkerShellArgs(t, target, "SHELL-TRUSTED-9a3e")
		e.Server.Push(ToolCallReply("c1", "shell", args), providertest.Text("done"))
		res := e.Run("write the file", "--approval", "auto", "--unsafe-auto-shell")
		res.RequireOK(t)
		if got := e.ReadFile("shell-side-effect.txt"); !strings.Contains(got, "SHELL-TRUSTED-9a3e") {
			t.Fatalf("trusted workspace did not run the command: %q", got)
		}
	})
}
