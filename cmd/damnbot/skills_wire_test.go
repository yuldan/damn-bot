package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/exec/classify"
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

// TestPaymentSkillDirs: declared payment, undeclared payment text, payment
// only in a (hidden) file, a symlinked skill, and a directory without
// SKILL.md are all gated; a broken skill fails closed (SEC10-3/8/9/10).
func TestPaymentSkillDirs(t *testing.T) {
	t.Parallel()
	dc := config.Default()
	dc.Skills.ProjectDiscovery = false // this test scans only <config>/skills
	cfg := t.TempDir()
	pay := writeSkill(t, cfg, "stripe-pay", "side_effects: [payment]\n", "Charge with the script.", nil)
	undeclared := writeSkill(t, cfg, "stripe-doc", "", "Use stripe payment_intents.", nil)
	filesOnly := writeSkill(t, cfg, "files-only", "", "See the helper.",
		map[string]string{"tools/pay.py": "import stripe\nstripe.PaymentIntent.create(amount=1)\n"})
	hidden := writeSkill(t, cfg, "hidden", "", "See the package.",
		map[string]string{".lib/pay.py": "import stripe\nstripe.PaymentIntent.create(amount=1)\n"})
	writeSkill(t, cfg, "plain", "", "Review the diff.", nil)
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("---\nname: linked\ndescription: Linked skill.\nside_effects: [payment]\n---\nLinked skill.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(cfg, "skills", "linked")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	loose := filepath.Join(cfg, "skills", "loose")
	if err := os.MkdirAll(filepath.Join(loose, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	dirs, err := paymentSkillDirs(context.Background(), &dc, cfg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{pay, undeclared, filesOnly, hidden, loose, filepath.Join(cfg, "skills", "linked")} {
		if !slices.Contains(dirs, want) {
			t.Errorf("missing gate dir %s in %v", want, dirs)
		}
	}
	// A SKILL.md symlink escaping the skill directory is rejected (SEC10-12).
	cfg2 := t.TempDir()
	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(out, "outside.md"), []byte("---\nname: escape\ndescription: d\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cfg2, "skills", "escape")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(out, "outside.md"), filepath.Join(dir, "SKILL.md")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, err := paymentSkillDirs(context.Background(), &dc, cfg2, cfg2); err == nil {
		t.Fatal("escaping SKILL.md symlink must fail closed")
	}

	bad := writeSkill(t, cfg, "broken", "", "", nil)
	if err := os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte("no front matter"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := paymentSkillDirs(context.Background(), &dc, cfg, cfg); err == nil {
		t.Fatal("a broken skill must fail closed")
	}
}

// TestPaymentSkillGateWired (SEC10-29 on Windows): with a payment skill installed under
// <config>/skills, a headless `run --approval auto --unsafe-auto-shell` turn
// whose tool call cds into that skill is denied and never executes
// (SEC10-1/SEC10-6, end to end).
func TestPaymentSkillGateWired(t *testing.T) {
	t.Parallel()
	cfgDir, dataDir := t.TempDir(), t.TempDir()
	marker := filepath.Join(dataDir, "ran")
	script, cmd := "scripts/charge.sh", ""
	if runtime.GOOS == "windows" {
		// SEC10-29: PowerShell is the real shell on Windows; the call
		// operator plus a payment-skill script must classify Monetary.
		script = "scripts/charge.ps1"
		cmd = `& '` + filepath.Join(writeSkill(t, cfgDir, "stripe-pay", "side_effects: [payment]\n", "Run scripts/charge.ps1.",
			map[string]string{script: "New-Item -ItemType File -LiteralPath '" + marker + "' -Force | Out-Null\n"}), "scripts", "charge.ps1") + `'`
	} else {
		pay := writeSkill(t, cfgDir, "stripe-pay", "side_effects: [payment]\n", "Run scripts/charge.sh.",
			map[string]string{script: "#!/bin/sh\ntouch " + marker + "\n"})
		cmd = `cd "` + filepath.Join(pay, "scripts") + `" && ./charge.sh`
	}
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
	o := &runOptions{model: "openai/gpt-fake", baseURL: srv.URL, approval: "auto", unsafeShell: true, providerAttempts: 1}
	model, err := startRunModel(t.Context(), &cfg, dataDir, o.model, o, red, cfgDir, t.TempDir(), nil)
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
	}, model, o.model, red, model.spill, &out); rerr != nil || code != 0 {
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

// TestMonetaryKeySymlink: a link and its target produce the same gate key,
// so /var vs /private/var (darwin) and link vs target cannot bypass it.
func TestMonetaryKeySymlink(t *testing.T) {
	t.Parallel()
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if a, b := monetaryKey(link), monetaryKey(target); a != b {
		t.Fatalf("link key %q != target key %q", a, b)
	}
}

// TestPaymentGateResolvedTarget: a command that names the resolved target of
// a symlinked payment skill is Monetary although the gate lists the link
// (canonical key makes both forms equal).
func TestPaymentGateResolvedTarget(t *testing.T) {
	t.Parallel()
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "pay-skill")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	env := &classify.Env{
		Roots: []string{target}, Home: "/home/u", Dialect: runtimeDialect(),
		MonetaryPaths: []string{link}, MonetaryKey: monetaryKey,
	}
	v := classify.Classify("cat "+filepath.Join(target, "scripts", "charge.sh"), env)
	if v.Class != classify.Monetary {
		t.Fatalf("class = %s, want monetary (%v)", v.Class, v.Reasons)
	}
}

// TestWorkspaceTrustedInertStoreEntries: the trust store is hand-editable, so
// a relative or malformed entry must be inert — it resolves to a canonical key
// that never matches a real workspace (untrusted, no error) instead of
// granting trust or failing every run. The CLI normalizer rejects relative
// input before it is written; this pins the reader's behaviour.
func TestWorkspaceTrustedInertStoreEntries(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	home := t.TempDir()
	configDir := filepath.Join(home, "damnbot")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	store := func(lines ...string) {
		t.Helper()
		body := strings.Join(lines, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(configDir, trustedProjectsFile), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	store("relative/dir", "malformed\x00path", "# comment", "   ")
	if ok, err := workspaceTrusted(&cfg, configDir, workspace, home); err != nil || ok {
		t.Fatalf("inert store entries: trusted = %v, err = %v; want false, nil", ok, err)
	}

	// Positive control: the same workspace registered by its absolute path is
	// trusted, so the assertion above is about the entries and not about a
	// function that always answers false.
	store(workspace)
	if ok, err := workspaceTrusted(&cfg, configDir, workspace, home); err != nil || !ok {
		t.Fatalf("absolute store entry: trusted = %v, err = %v; want true, nil", ok, err)
	}
}
