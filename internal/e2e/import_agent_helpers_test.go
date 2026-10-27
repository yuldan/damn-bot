package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assertRejectedImport proves a rejected run exits non-zero, names the
// importer, leaves the home byte-identical and never creates the data dir.
func assertRejectedImport(t *testing.T, e *Env, args []string, stderrContains string) {
	t.Helper()
	before := hashTree(t, e.home)
	res := importExec(e, args...)
	if res.Code == 0 {
		t.Fatalf("rejected import exited 0:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stderr, stderrContains) {
		t.Errorf("stderr = %q, want %q", res.Stderr, stderrContains)
	}
	if after := hashTree(t, e.home); after != before {
		t.Errorf("rejected import changed state")
	}
	requireNoDataDir(t, e)
}

// addEscapingSkill links root/skills/escaped/SKILL.md to a file outside root.
func addEscapingSkill(t *testing.T, root string) {
	t.Helper()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "SKILL.md"),
		[]byte("---\nname: escaped\ndescription: escaped\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	evil := filepath.Join(root, "skills", "escaped")
	if err := os.MkdirAll(evil, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "SKILL.md"), filepath.Join(evil, "SKILL.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
}
