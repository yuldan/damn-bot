package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/skills"
)

func runSkillsCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = runSkills(args, &out, &errb)
	return code, out.String(), errb.String()
}

// TestSkillsCLIListInspectCheck: list/inspect/check/config.
func TestSkillsCLIListInspectCheck(t *testing.T) {
	dir := bundleConfig(t)
	data := t.TempDir()
	if code, out, e := runSkillsCLI(t, "list", "--config-dir", dir, "--data-dir", data); code != 0 || !strings.Contains(out, "go-lint") || !strings.Contains(out, "shellcheck") {
		t.Fatalf("list = %d %q (%s)", code, out, e)
	}
	if code, out, e := runSkillsCLI(t, "inspect", "go-lint", "--config-dir", dir, "--data-dir", data); code != 0 || !strings.Contains(out, "name: go-lint") || !strings.Contains(out, "body") {
		t.Fatalf("inspect = %d %q (%s)", code, out, e)
	}
	if code, _, _ := runSkillsCLI(t, "inspect", "ghost", "--config-dir", dir, "--data-dir", data); code != 1 {
		t.Errorf("inspect missing = %d, want 1", code)
	}
	if code, out, e := runSkillsCLI(t, "check", "--config-dir", dir, "--data-dir", data); code != 0 || !strings.Contains(out, "ok: 2 skills") {
		t.Fatalf("check = %d %q (%s)", code, out, e)
	}
	if code, out, _ := runSkillsCLI(t, "config", "--config-dir", dir, "--data-dir", data); code != 0 || !strings.Contains(out, "inline_shell") {
		t.Fatalf("config = %d %q", code, out)
	}
}

// TestSkillsIndexDiffAudit: update/index check, delta detection, diff and audit.
func TestSkillsIndexDiffAudit(t *testing.T) {
	dir := bundleConfig(t)
	data := t.TempDir()
	if code, out, e := runSkillsCLI(t, "update", "--config-dir", dir, "--data-dir", data); code != 0 || !strings.Contains(out, "added go-lint") {
		t.Fatalf("update = %d %q (%s)", code, out, e)
	}
	if code, out, _ := runSkillsCLI(t, "index", "--check", "--config-dir", dir, "--data-dir", data); code != 0 || !strings.Contains(out, "up to date") {
		t.Fatalf("index check = %d %q", code, out)
	}
	other := filepath.Join(dir, "skills", "new-skill")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "SKILL.md"), []byte("---\nname: new-skill\ndescription: new\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := runSkillsCLI(t, "index", "--check", "--config-dir", dir, "--data-dir", data); code != 1 || !strings.Contains(out, "added new-skill") {
		t.Fatalf("index delta = %d %q, want exit 1 with added", code, out)
	}
	if err := skills.AppendLedger(data, &skills.LedgerEntry{At: 1, Name: "go-lint", SHA256: "old", Path: "/p", Source: "user", Action: "indexed"}); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := runSkillsCLI(t, "diff", "go-lint", "--config-dir", dir, "--data-dir", data); code != 0 || !strings.Contains(out, "changed") {
		t.Fatalf("diff = %d %q", code, out)
	}
	if code, out, _ := runSkillsCLI(t, "audit", "--config-dir", dir, "--data-dir", data); code != 0 || !strings.Contains(out, "go-lint") || !strings.Contains(out, "indexed") {
		t.Fatalf("audit = %d %q", code, out)
	}
}

// TestSkillsInstallUninstall: install copies a valid skill, refuses duplicates
// and symlinks; uninstall needs --yes.
func TestSkillsInstallUninstall(t *testing.T) {
	dir := t.TempDir()
	src := t.TempDir()
	doc := "---\nname: installed\ndescription: from source\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, e := runSkillsCLI(t, "install", src, "--config-dir", dir, "--data-dir", t.TempDir()); code != 0 || !strings.Contains(out, "installed installed") {
		t.Fatalf("install = %d %q (%s)", code, out, e)
	}
	if code, _, e := runSkillsCLI(t, "install", src, "--config-dir", dir, "--data-dir", t.TempDir()); code != 2 || !strings.Contains(e, "already exists") {
		t.Fatalf("duplicate install = %d %q", code, e)
	}
	if code, _, _ := runSkillsCLI(t, "uninstall", "installed", "--config-dir", dir, "--data-dir", t.TempDir()); code != 2 {
		t.Errorf("uninstall without --yes = %d, want 2", code)
	}
	if code, out, _ := runSkillsCLI(t, "uninstall", "installed", "--yes", "--config-dir", dir, "--data-dir", t.TempDir()); code != 0 || !strings.Contains(out, "uninstalled installed") {
		t.Fatalf("uninstall = %d %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", "installed")); !os.IsNotExist(err) {
		t.Errorf("skill directory survived uninstall: %v", err)
	}
	// A symlink anywhere in the source tree is refused.
	evil := t.TempDir()
	if err := os.WriteFile(filepath.Join(evil, "SKILL.md"), []byte("---\nname: evil\ndescription: e\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(evil, "link")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if code, _, e := runSkillsCLI(t, "install", evil, "--config-dir", dir, "--data-dir", t.TempDir()); code != 1 || !strings.Contains(e, "non-regular") {
		t.Fatalf("symlink install = %d %q", code, e)
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", "evil")); !os.IsNotExist(err) {
		t.Errorf("partial install left behind: %v", err)
	}
}

// TestSkillsReset: removing the index needs --yes and forces a rebuild.
func TestSkillsReset(t *testing.T) {
	dir := bundleConfig(t)
	data := t.TempDir()
	if code, _, _ := runSkillsCLI(t, "update", "--config-dir", dir, "--data-dir", data); code != 0 {
		t.Fatal("update failed")
	}
	if code, _, _ := runSkillsCLI(t, "reset", "--config-dir", dir, "--data-dir", data); code != 2 {
		t.Errorf("reset without --yes = %d, want 2", code)
	}
	if code, out, _ := runSkillsCLI(t, "reset", "--yes", "--config-dir", dir, "--data-dir", data); code != 0 || !strings.Contains(out, "reset skills index") {
		t.Fatalf("reset = %d %q", code, out)
	}
	if code, _, _ := runSkillsCLI(t, "index", "--check", "--config-dir", dir, "--data-dir", data); code != 1 {
		t.Errorf("index after reset = %d, want 1 (stale)", code)
	}
}
