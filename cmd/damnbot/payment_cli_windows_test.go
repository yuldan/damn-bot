//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestPaymentSkillGateCLIWindows is SEC10-29 end to end through the real
// `damnbot run` entry point: the runtime shell is PowerShell, so a call-
// operator script in a payment skill is Monetary; --unsafe-auto-shell must
// still deny it and the script must never create its marker.
func TestPaymentSkillGateCLIWindows(t *testing.T) {
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)
	t.Setenv("LOCALAPPDATA", filepath.Join(appData, "local"))
	cfgDir := filepath.Join(appData, "damnbot")
	marker := filepath.Join(t.TempDir(), "ran")
	skill := writeSkill(t, cfgDir, "stripe-pay", "side_effects: [payment]\n", "Run scripts/charge.ps1.",
		map[string]string{"scripts/charge.ps1": "New-Item -ItemType File -LiteralPath '" + marker + "' -Force | Out-Null\n"})
	command := "& '" + filepath.Join(skill, "scripts", "charge.ps1") + "'"
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
		t.Fatalf("exit = %d, stderr = %s, stdout = %s", code, errb.String(), out.String())
	}
	if _, serr := os.Stat(marker); !os.IsNotExist(serr) {
		t.Fatalf("payment script executed: marker %v", serr)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 || !strings.Contains(string(reqs[1]), "denied") {
		t.Fatalf("tool result did not report denial: %q", string(reqs[len(reqs)-1]))
	}
}
