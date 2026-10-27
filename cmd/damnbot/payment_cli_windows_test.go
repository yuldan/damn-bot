//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestPaymentSkillGateCLIWindows is SEC10-29/30 end to end through the real
// `damnbot run` entry point: the runtime shell is PowerShell, so call-operator,
// Set-Location and python forms naming a payment skill are Monetary and
// --unsafe-auto-shell still denies them. The no-declaration control proves the
// harness really executes the script when the gate does not fire.
func TestPaymentSkillGateCLIWindows(t *testing.T) {
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)
	t.Setenv("LOCALAPPDATA", filepath.Join(appData, "local"))
	cfgDir := filepath.Join(appData, "damnbot")
	dir := t.TempDir()
	// --unsafe-auto-shell only auto-runs in a trusted workspace (SPEC §14):
	// the child's cwd is the temp workspace named in the trust store.
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "trusted-projects"), []byte(dir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	marker := func(name string) string { return filepath.Join(dir, name) }
	psBody := func(m string) string {
		return "New-Item -ItemType File -Path '" + m + "' -Force | Out-Null\n"
	}
	pay := writeSkill(t, cfgDir, "stripe-pay", "side_effects: [payment]\n", "Run the scripts.", map[string]string{
		"scripts/charge.ps1": psBody(marker("pay-call")),
		"scripts/charge.py":  "open(r\"" + marker("pay-python") + "\", \"w\").write(\"x\")\n",
	})
	// The control is a workspace script, not a skill script: every scanned
	// <config>/skills directory is gated regardless of quarantine or a
	// missing `side_effects` (SEC10-13, ADR-168), so an undeclared skill
	// script is Monetary too and cannot prove execution.
	work := filepath.Join(dir, "control")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	plain := work
	if err := os.MkdirAll(filepath.Join(work, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "scripts", "charge.ps1"), []byte(psBody(marker("plain"))), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		command string
		marker  string
		creates bool
		force51 bool
	}{
		{"call-operator", "& '" + filepath.Join(pay, "scripts", "charge.ps1") + "'", marker("pay-call"), false, false},
		{"set-location", "Set-Location '" + filepath.Join(pay, "scripts") + "'; .\\charge.ps1", marker("pay-call"), false, false},
		{"python", "python '" + filepath.Join(pay, "scripts", "charge.py") + "'", marker("pay-python"), false, false},
		{"powershell51", "& '" + filepath.Join(pay, "scripts", "charge.ps1") + "'", marker("pay-call"), false, true},
		{"negative-control", "& '" + filepath.Join(plain, "scripts", "charge.ps1") + "'", marker("plain"), true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.force51 {
				t.Setenv("PATH", powershell51(t))
				if got := exec.RuntimeShellKind(); got != exec.ShellPowerShell {
					t.Fatalf("runtime shell = %v, want PowerShell", got)
				}
			}
			if tc.creates {
				// The control must start clean, then run without a gate hit.
				if err := os.Remove(tc.marker); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
			res := runShellCLI(t, tc.command)
			if _, err := os.Stat(tc.marker); tc.creates != (err == nil) {
				t.Fatalf("marker created = %v, want %v (stat %v); tool=%q stderr=%q", err == nil, tc.creates, err, res.tool(), res.stderr)
			}
			denied := strings.Contains(res.tool(), "denied")
			if denied == tc.creates {
				t.Fatalf("tool result denied = %v, want %v: %q stderr=%q", denied, !tc.creates, res.tool(), res.stderr)
			}
		})
	}
}

type cliResult struct {
	reqs   [][]byte
	stderr string
}

func (r cliResult) tool() string { return string(r.reqs[len(r.reqs)-1]) }

func runShellCLI(t *testing.T, command string) cliResult {
	t.Helper()
	args, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	call := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"shell","arguments":` +
		jsonString(string(args)) + `}}]}}]}`
	end := `{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`
	srv := providertest.New(t, nil,
		providertest.Reply{Frames: []string{call, end, "[DONE]"}},
		providertest.Text("stopped"),
	)
	var out, errb bytes.Buffer
	code := run([]string{
		"run", "-p", "pay", "--model", "openai/gpt-fake", "--base-url", srv.URL,
		"--data-dir", t.TempDir(), "--output", "json", "--approval", "auto",
		"--unsafe-auto-shell", "--provider-attempts", "1",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("run: exit %d, stderr %s, stdout %s", code, errb.String(), out.String())
	}
	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(reqs))
	}
	return cliResult{reqs: reqs, stderr: errb.String()}
}

func powershell51(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0")
	if _, err := os.Stat(filepath.Join(dir, "powershell.exe")); err != nil {
		t.Fatalf("Windows PowerShell 5.1 not found: %v", err)
	}
	return dir
}
