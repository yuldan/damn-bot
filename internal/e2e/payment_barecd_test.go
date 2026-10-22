package e2e

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// workspaceScript writes a script INSIDE the child's workspace and returns the
// relative command that runs it (argv0 relative, the SEC10-1/35 shape).
func workspaceScript(t *testing.T, e *Env, marker string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		e.WriteFile("rel.ps1", "New-Item -ItemType File -Path '"+
			strings.ReplaceAll(marker, "'", "''")+"' -Force | Out-Null\n")
		return `.\rel.ps1`
	}
	p := e.WriteFile("rel.sh", "#!/bin/sh\ntouch '"+strings.ReplaceAll(marker, "'", `'\''`)+"'\n")
	if err := os.Chmod(p, 0o700); err != nil { //nolint:gosec // test executable needs the exec bit
		t.Fatal(err)
	}
	return "./rel.sh"
}

// bareCdCommand is the `cd; <relative path>` that each platform's shell really
// runs and the classifier really reads: bash tracks the bare cd to $HOME (so
// the payment skill script under $HOME must stay Monetary, SEC10-30), while
// PowerShell leaves the cwd untracked (5.1 does not move, 7 moves to $HOME),
// so a workspace-relative argv0 must be Unknown (SEC10-35/36).
func bareCdCommand(t *testing.T, e *Env, marker string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return "cd; " + workspaceScript(t, e, marker)
	}
	script, _ := skillScript(t, e, "stripe-pay", true, marker)
	rel, err := filepath.Rel(e.home, script)
	if err != nil {
		t.Fatal(err)
	}
	// Quoted: the macOS config dir is "Application Support", so the relative
	// path contains a space and an unquoted form would split into arguments.
	return "cd; './" + rel + "'"
}

// TestBareCdRelativeScriptNotAuto (SEC10-30/35/36, aturan owner; e2e biner
// asli): a relative argv0 after a bare `cd` never auto-runs in
// `--unsafe-auto-shell` — the bash side resolves it against the tracked $HOME
// and must still hit the payment gate, the PowerShell side cannot track the
// cwd at all. The same shape without the bare cd auto-runs, which is the
// negative control proving the harness executes it.
func TestBareCdRelativeScriptNotAuto(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		bare   bool
		denied bool
	}{
		{"bare-cd-relative", true, true},
		{"negative-control", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			marker := ""
			e := NewFunc(t, func(e *Env) []providertest.Reply {
				marker = filepath.Join(e.Workdir, "ran")
				command := workspaceScript(t, e, marker)
				if tc.bare {
					command = bareCdCommand(t, e, marker)
				}
				return []providertest.Reply{
					ToolCallReply("c1", "shell", `{"command":`+strconv.Quote(command)+`}`),
					providertest.Text("done"),
				}
			})
			res := e.Run("bare cd gate", "--approval", "auto", "--unsafe-auto-shell")
			res.RequireOK(t)
			if got := len(e.Requests()); got != 2 {
				t.Fatalf("provider calls = %d, want 2", got)
			}
			tool := e.RequestText(t, 1)
			if got := strings.Contains(tool, "denied"); got != tc.denied {
				t.Fatalf("tool result denied = %v, want %v: %s", got, tc.denied, tool)
			}
			_, err := os.Stat(marker)
			switch {
			case tc.denied && err == nil:
				t.Fatalf("script after bare cd ran despite the gate: %s", tool)
			case !tc.denied && err != nil:
				t.Fatalf("negative control did not run (%v): %s", err, tool)
			}
		})
	}
}
