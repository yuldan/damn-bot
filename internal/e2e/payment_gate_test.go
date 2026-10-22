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

// skillScript installs a skill in the child's config directory and returns the
// path of its script plus the command that runs it. declared adds
// `side_effects: [payment]`; the script writes marker when it executes.
func skillScript(t *testing.T, e *Env, name string, declared bool, marker string) (string, string) {
	t.Helper()
	dir := filepath.Join(e.ConfigDir(), "skills", name)
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	front := ""
	if declared {
		front = "side_effects: [payment]\n"
	}
	md := "---\nname: " + name + "\ndescription: e2e skill\n" + front + "---\nRun the scripts.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		p := filepath.Join(dir, "scripts", "charge.ps1")
		body := "New-Item -ItemType File -Path '" + strings.ReplaceAll(marker, "'", "''") + "' -Force | Out-Null\n"
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p, "& '" + p + "'"
	}
	p := filepath.Join(dir, "scripts", "charge.sh")
	body := "#!/bin/sh\ntouch '" + strings.ReplaceAll(marker, "'", `'\''`) + "'\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o700); err != nil { //nolint:gosec // test executable needs the exec bit
		t.Fatal(err)
	}
	return p, "'" + p + "'"
}

// TestPaymentSkillGateCLI (SEC10 gate, aturan owner; e2e biner asli): a script
// of a skill declaring `side_effects: [payment]` is Monetary and never runs
// under `--unsafe-auto-shell`, while the same shape for an undeclared skill
// runs — the negative control proves the harness really executes it.
func TestPaymentSkillGateCLI(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		declared bool
		denied   bool
	}{
		{"payment-skill", true, true},
		{"negative-control", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			marker := ""
			e := NewFunc(t, func(e *Env) []providertest.Reply {
				marker = filepath.Join(e.Workdir, "ran")
				_, command := skillScript(t, e, "stripe-pay", tc.declared, marker)
				return []providertest.Reply{
					ToolCallReply("c1", "shell", `{"command":`+strconv.Quote(command)+`}`),
					providertest.Text("done"),
				}
			})
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
