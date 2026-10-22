package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
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
	dirs, err := paymentSkillDirs(cfg)
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
	if _, err := paymentSkillDirs(cfg2); err == nil {
		t.Fatal("escaping SKILL.md symlink must fail closed")
	}

	bad := writeSkill(t, cfg, "broken", "", "", nil)
	if err := os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte("no front matter"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := paymentSkillDirs(cfg); err == nil {
		t.Fatal("a broken skill must fail closed")
	}
}

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
