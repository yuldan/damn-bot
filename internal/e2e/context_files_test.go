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

// decodedRequest decodes the first provider request and joins every string in
// it, so assertions read the text the model sees: in the raw body the wrapper
// marker and every newline are JSON-escaped, which would make a literal
// search for <untrusted source="workspace"> silently vacuous.
func decodedRequest(t *testing.T, e *Env) string {
	t.Helper()
	reqs := e.Requests()
	if len(reqs) == 0 {
		t.Fatal("no provider request recorded")
	}
	var v any
	if err := json.Unmarshal(reqs[0], &v); err != nil {
		t.Fatalf("request 0 is not JSON: %v", err)
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
func writeMarkerShellArgs(t *testing.T, e *Env, rel, marker string) string {
	t.Helper()
	// Windows gets a quote-free command and an explicit cwd. The runner starts
	// cmd.exe with the session cwd (internal/exec/oneshot.go Spec.Dir) and
	// cmd.exe re-parses the raw command line, so Go's escaping of a quoted
	// argument (`\"`) survives literally and any quote in the fixture would
	// redirect into a file literally named `\"name\"`; pinning `cwd` removes
	// the other unknown, which directory the relative target lands in.
	payload := struct {
		Command string `json:"command"`
		Cwd     string `json:"cwd,omitempty"`
	}{Command: "sh -c 'echo " + marker + " > \"" + rel + "\"'"}
	if runtime.GOOS == "windows" {
		if strings.ContainsAny(rel, " ") {
			t.Fatalf("writeMarkerShellArgs: Windows needs a space-free path, got %q", rel)
		}
		// The relative target keeps both cases the same command; the explicit
		// cwd pins the directory it resolves against (the one-shot runs through
		// `cmd /d /s /c`, internal/exec/oneshot.go), and the trailing `cd`
		// prints the cwd cmd.exe actually used into the tool result, so a
		// failure says whether the directory or the call itself was wrong.
		// `%CD%` is deliberately absent: it makes classify label the command
		// Unknown (denied) instead of Mutating, which the scenarios assert on.
		payload.Command = "echo " + marker + "> " + rel + " & cd"
		payload.Cwd = e.Workdir
	}
	raw, err := json.Marshal(payload)
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
		req := decodedRequest(t, e)
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
		req := decodedRequest(t, e)
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
		req := decodedRequest(t, e)
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
		if blocks := workspaceWrapped(decodedRequest(t, e)); len(blocks) != 0 {
			t.Fatalf("wrapper without a context file: %q", blocks)
		}
	})
}

// TestE2EUntrustedWorkspaceShellGate: the same trust decision gates the shell
// auto paths (SPEC §14/§10) — in an untrusted workspace --unsafe-auto-shell is
// dead, and the identical command runs once the workspace is registered. It is
// a separate test so each function stays readable (gocognit).
func TestE2EUntrustedWorkspaceShellGate(t *testing.T) {
	t.Parallel()

	// The trust decision also gates the shell auto paths (SPEC §14/§10): in an
	// untrusted workspace --unsafe-auto-shell and shell_allow are dead, and the
	// same command runs once the workspace is registered.
	t.Run("untrusted workspace disables the shell auto paths", func(t *testing.T) {
		t.Parallel()
		e := New(t)
		// Relative target: the shell runs with the workspace as cwd, so the
		// command and the assertion agree on every OS (an absolute temp path
		// is the logical /var form on macOS while the sandbox root is the
		// resolved /private/var one).
		args := writeMarkerShellArgs(t, e, "shell-side-effect.txt", "SHELL-UNTRUSTED-9a3e")
		e.Server.Push(ToolCallReply("c1", "shell", args), providertest.Text("done"))
		res := e.Run("write the file", "--approval", "auto", "--unsafe-auto-shell", "--output", "stream-json")
		if res.Code != 0 {
			t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
		}
		if _, err := os.Stat(e.Path("shell-side-effect.txt")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("untrusted workspace ran the command: %v", err)
		}
		if !strings.Contains(res.Stdout, "shell is not allowed in this mode") {
			t.Fatalf("untrusted workspace did not refuse the shell call:\n%s", res.Stdout)
		}
	})

	t.Run("trusted workspace runs the same command", func(t *testing.T) {
		t.Parallel()
		e := New(t)
		trustWorkspace(t, e)
		args := writeMarkerShellArgs(t, e, "shell-side-effect.txt", "SHELL-TRUSTED-9a3e")
		e.Server.Push(ToolCallReply("c1", "shell", args), providertest.Text("done"))
		res := e.Run("write the file", "--approval", "auto", "--unsafe-auto-shell", "--output", "stream-json")
		res.RequireOK(t)
		got, rerr := os.ReadFile(filepath.Join(e.Workdir, "shell-side-effect.txt"))
		if rerr != nil || !strings.Contains(string(got), "SHELL-TRUSTED-9a3e") {
			names, derr := os.ReadDir(e.Workdir)
			var list []string
			for _, n := range names {
				list = append(list, n.Name())
			}
			real, realErr := filepath.EvalSymlinks(e.Workdir)
			t.Fatalf("trusted workspace did not run the command: err=%v content=%q\nworkdir=%q eval=%q evalErr=%v entries=%v readdirErr=%v\nstdout:\n%s\nstderr:\n%s",
				rerr, got, e.Workdir, real, realErr, list, derr, res.Stdout, res.Stderr)
		}
	})
}
