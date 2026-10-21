package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/provider/providertest"
	"github.com/damnbot/damnbot/internal/redact"
)

// writeSkill installs one SKILL.md (and optional files) under
// <configDir>/skills/<name>.
func writeSkill(t *testing.T, configDir, name, front, body string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(configDir, "skills", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: test skill\n" + front + "---\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	for p, content := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestPaymentSkillDirs: declared payment, undeclared payment text and plain
// skills; a broken skill fails closed (SEC10-3, SEC10-6).
func TestPaymentSkillDirs(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	pay := writeSkill(t, cfg, "stripe-pay", "side_effects: [payment]\n", "Charge with the script.", nil)
	undeclared := writeSkill(t, cfg, "stripe-doc", "", "Use stripe payment_intents.", nil)
	writeSkill(t, cfg, "plain", "", "Review the diff.", nil)
	dirs, err := paymentSkillDirs(cfg)
	if err != nil || len(dirs) != 2 || !slices.Contains(dirs, pay) || !slices.Contains(dirs, undeclared) {
		t.Fatalf("dirs = %v, err %v", dirs, err)
	}
	bad := writeSkill(t, cfg, "broken", "", "", nil)
	if err := os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte("no front matter"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := paymentSkillDirs(cfg); err == nil {
		t.Fatal("a broken skill must fail closed")
	}
}

// TestPaymentSkillGateWired: with a payment skill installed under
// <config>/skills, a headless `run --approval auto --unsafe-auto-shell` turn
// whose tool call cds into that skill is denied and never executes
// (SEC10-1/SEC10-6, end to end).
func TestPaymentSkillGateWired(t *testing.T) {
	t.Parallel()
	cfgDir, dataDir := t.TempDir(), t.TempDir()
	marker := filepath.Join(dataDir, "ran")
	pay := writeSkill(t, cfgDir, "stripe-pay", "side_effects: [payment]\n", "Run scripts/charge.sh.",
		map[string]string{"scripts/charge.sh": "#!/bin/sh\ntouch " + marker + "\n"})
	cmd := "cd " + filepath.Join(pay, "scripts") + "; ./charge.sh"
	args, err := json.Marshal(map[string]string{"command": cmd})
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
	cfg := config.Default()
	red := redact.New(redact.Options{})
	spill := diskSpill{dir: filepath.Join(dataDir, "spill")}
	o := &runOptions{model: "openai/gpt-fake", baseURL: srv.URL, approval: "auto", unsafeShell: true, providerAttempts: 1}
	model, err := startRunModel(t.Context(), &cfg, dataDir, o.model, o, red, spill, cfgDir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := model.close(t.Context()); cerr != nil {
			t.Error(cerr)
		}
	}()
	var out bytes.Buffer
	if code, rerr := runTurn(t.Context(), &cfg, &runOptions{
		prompt: "pay", model: o.model, baseURL: o.baseURL, providerAttempts: 1, output: "json",
		approval: "auto", unsafeShell: true,
	}, model, o.model, red, spill, &out); rerr != nil || code != 0 {
		t.Fatalf("turn: code %d, err %v", code, rerr)
	}
	if _, serr := os.Stat(marker); !os.IsNotExist(serr) {
		t.Fatalf("payment script executed: marker %v", serr)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 || !strings.Contains(string(reqs[1]), "denied") {
		t.Fatalf("tool result did not report denial: %q", string(reqs[len(reqs)-1]))
	}
}

// jsonString renders s as a JSON string literal for a scripted provider frame.
func jsonString(s string) string { return strconv.Quote(s) }
