package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCLISkill writes one SKILL.md under root/<name>.
func writeCLISkill(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	doc := "---\nname: " + name + "\ndescription: " + name + " skill\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSkillsCLI: list/check/update/install/uninstall through the real binary,
// including the broken-skill and --yes negative controls.
func TestSkillsCLI(t *testing.T) {
	t.Parallel()
	e := New(t)
	configDir := t.TempDir()
	dataDir := t.TempDir()
	writeCLISkill(t, filepath.Join(configDir, "skills"), "lint", "body")
	run := func(args ...string) Result {
		return e.Exec(append(args, "--config-dir", configDir, "--data-dir", dataDir)...)
	}
	list := run("skills", "list")
	list.RequireOK(t)
	if !strings.Contains(list.Stdout, "lint") {
		t.Errorf("list = %q", list.Stdout)
	}
	check := run("skills", "check")
	check.RequireOK(t)
	if !strings.Contains(check.Stdout, "ok: 1 skills") {
		t.Errorf("check = %q", check.Stdout)
	}
	update := run("skills", "update")
	update.RequireOK(t)
	if !strings.Contains(update.Stdout, "added lint") {
		t.Errorf("update = %q", update.Stdout)
	}
	if again := run("skills", "index", "--check"); again.Code != 0 || !strings.Contains(again.Stdout, "up to date") {
		t.Errorf("index check = %d %q", again.Code, again.Stdout)
	}
	// A skill with a dangling reference fails check closed.
	bad := filepath.Join(configDir, "skills", "bad")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte("---\nname: bad\ndescription: bad\nrequires: [ghost]\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if broken := run("skills", "check"); broken.Code != 1 {
		t.Errorf("check with dangling ref = %d, want 1", broken.Code)
	}
	if err := os.RemoveAll(bad); err != nil {
		t.Fatal(err)
	}
	// install from a source dir, then refuse duplicate and uninstall.
	src := t.TempDir()
	writeCLISkill(t, src, "installed", "body")
	install := run("skills", "install", filepath.Join(src, "installed"))
	install.RequireOK(t)
	if !strings.Contains(install.Stdout, "installed installed") {
		t.Errorf("install = %q", install.Stdout)
	}
	if dup := run("skills", "install", filepath.Join(src, "installed")); dup.Code != 2 {
		t.Errorf("duplicate install = %d, want 2", dup.Code)
	}
	if noYes := run("skills", "uninstall", "installed"); noYes.Code != 2 {
		t.Errorf("uninstall without --yes = %d, want 2", noYes.Code)
	}
	if del := run("skills", "uninstall", "installed", "--yes"); !strings.Contains(del.Stdout, "uninstalled installed") {
		t.Errorf("uninstall = %q", del.Stdout)
	}
}
