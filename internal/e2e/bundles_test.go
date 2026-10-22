package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBundlesCLI: bundle create/list/show/delete through the child binary
// with negative controls (unknown skill, missing --yes).
func TestBundlesCLI(t *testing.T) {
	t.Parallel()
	e := New(t)
	dir := t.TempDir()
	for _, name := range []string{"go-lint", "shellcheck"} {
		p := filepath.Join(dir, "skills", name)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		doc := "---\nname: " + name + "\ndescription: " + name + " skill\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bad := e.Exec("bundles", "create", "lint", "go-lint", "ghost", "--config-dir", dir)
	if bad.Code != 2 || !strings.Contains(bad.Stderr, "unknown skill") {
		t.Fatalf("unknown skill = %d %q", bad.Code, bad.Stderr)
	}
	create := e.Exec("bundles", "create", "lint", "go-lint", "shellcheck", "--config-dir", dir)
	create.RequireOK(t)
	if !strings.Contains(create.Stdout, "created lint") {
		t.Errorf("create = %q", create.Stdout)
	}
	list := e.Exec("bundles", "list", "--config-dir", dir)
	list.RequireOK(t)
	if !strings.Contains(list.Stdout, "lint: go-lint, shellcheck") {
		t.Errorf("list = %q", list.Stdout)
	}
	show := e.Exec("bundles", "show", "lint", "--config-dir", dir)
	show.RequireOK(t)
	if !strings.Contains(show.Stdout, "go-lint") {
		t.Errorf("show = %q", show.Stdout)
	}
	if noYes := e.Exec("bundles", "delete", "lint", "--config-dir", dir); noYes.Code != 2 {
		t.Fatalf("delete without --yes = %d", noYes.Code)
	}
	del := e.Exec("bundles", "delete", "lint", "--yes", "--config-dir", dir)
	del.RequireOK(t)
	if after := e.Exec("bundles", "list", "--config-dir", dir); strings.TrimSpace(after.Stdout) != "" {
		t.Errorf("bundle survived delete:\n%s", after.Stdout)
	}
}
