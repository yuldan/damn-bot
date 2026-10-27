package e2e

import (
	"errors"
	"io/fs"
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

// assertAgentDryRun proves a dry run reports the plan, writes nothing and
// leaves the home byte-identical.
func assertAgentDryRun(t *testing.T, e *Env, args []string, wantMemory string) {
	t.Helper()
	before := hashTree(t, e.home)
	res := importExec(e, args...)
	res.RequireOK(t)
	if !strings.Contains(res.Stdout, wantMemory) || !strings.Contains(res.Stdout, "dry run: nothing was written") {
		t.Errorf("dry run output:\n%s", res.Stdout)
	}
	if after := hashTree(t, e.home); after != before {
		t.Errorf("dry run changed state: %s -> %s", before, after)
	}
	requireNoDataDir(t, e)
}

// assertUnsupportedOnlyNoWrite proves an unsupported --only exits 2 without
// creating the data dir or changing the home.
func assertUnsupportedOnlyNoWrite(t *testing.T, e *Env, args []string) {
	t.Helper()
	before := hashTree(t, e.home)
	if res := importExec(e, args...); res.Code != 2 {
		t.Errorf("%v = %d, want 2", args, res.Code)
	}
	if after := hashTree(t, e.home); after != before {
		t.Errorf("unsupported --only changed state")
	}
	requireNoDataDir(t, e)
	if _, err := os.Stat(importHermesDataDir(e)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("unsupported --only wrote data dir: %v", err)
	}
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
