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

// markerScript writes the script the scenario runs into dir/scripts and
// returns the command that runs it; the script writes marker when it
// executes. PowerShell and POSIX get their native spelling.
func markerScript(t *testing.T, dir, marker string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		p := filepath.Join(dir, "scripts", "charge.ps1")
		body := "New-Item -ItemType File -Path '" + strings.ReplaceAll(marker, "'", "''") + "' -Force | Out-Null\n"
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return "& '" + p + "'"
	}
	p := filepath.Join(dir, "scripts", "charge.sh")
	body := "#!/bin/sh\ntouch '" + strings.ReplaceAll(marker, "'", `'\''`) + "'\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o700); err != nil { //nolint:gosec // test executable needs the exec bit
		t.Fatal(err)
	}
	return "'" + p + "'"
}

// paymentSkill installs a skill declaring `side_effects: [payment]` under the
// child's config directory and returns the command that runs its script.
func paymentSkill(t *testing.T, e *Env, name, marker string) string {
	t.Helper()
	dir := filepath.Join(e.ConfigDir(), "skills", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: e2e skill\nside_effects: [payment]\n---\nRun the scripts.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	return markerScript(t, dir, marker)
}

// TestPaymentSkillGateCLI (SEC10 gate, aturan owner; e2e biner asli): a script
// of a skill declaring `side_effects: [payment]` is Monetary and never runs
// under `--unsafe-auto-shell`. The negative control runs the same command
// shape on a script inside the workspace, which is not gated: it proves the
// harness really executes the shell call, so "denied" above is a decision and
// not a harness failure. An undeclared skill would not work as the control:
// every directory under <config>/skills is gated regardless of quarantine
// (SEC10-13/ADR-168), so its script is Monetary too.
func TestPaymentSkillGateCLI(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		skill  bool
		denied bool
	}{
		{"payment-skill", true, true},
		{"workspace-control", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			marker := ""
			e := NewFunc(t, func(e *Env) []providertest.Reply {
				marker = filepath.Join(e.Workdir, "ran")
				command := markerScript(t, e.Workdir, marker)
				if tc.skill {
					command = paymentSkill(t, e, "stripe-pay", marker)
				}
				return []providertest.Reply{
					ToolCallReply("c1", "shell", `{"command":`+strconv.Quote(command)+`}`),
					providertest.Text("done"),
				}
			})
			// --unsafe-auto-shell only auto-runs in a trusted workspace
			// (SPEC §14), so the negative control needs the harness trust
			// store; the refusal leg must hold regardless of trust.
			trustWorkspace(t, e)
			res := e.Run("payment gate", "--approval", "auto", "--unsafe-auto-shell")
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
				t.Fatalf("payment script ran despite denial: %s", tool)
			case !tc.denied && err != nil:
				t.Fatalf("negative control did not run (%v): %s", err, tool)
			}
		})
	}
}
