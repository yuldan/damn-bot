package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runBundlesCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = runBundles(args, &out, &errb)
	return code, out.String(), errb.String()
}

// bundleConfig creates a config dir with two installed skills.
func bundleConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"go-lint", "shellcheck"} {
		path := filepath.Join(dir, "skills", name)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		doc := "---\nname: " + name + "\ndescription: " + name + " skill\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestBundlesCreateShow: empty list, validation, create, duplicate, list/show.
func TestBundlesCreateShow(t *testing.T) {
	dir := bundleConfig(t)
	if code, out, e := runBundlesCLI(t, "list", "--config-dir", dir); code != 0 || out != "" {
		t.Fatalf("empty list = %d %q (%s)", code, out, e)
	}
	if code, _, e := runBundlesCLI(t, "create", "lint", "go-lint", "ghost", "--config-dir", dir); code != 2 || !strings.Contains(e, "unknown skill") {
		t.Fatalf("unknown skill = %d %q", code, e)
	}
	if code, out, e := runBundlesCLI(t, "create", "lint", "go-lint", "shellcheck", "--config-dir", dir); code != 0 || !strings.Contains(out, "created lint") {
		t.Fatalf("create = %d %q (%s)", code, out, e)
	}
	if code, _, e := runBundlesCLI(t, "create", "lint", "go-lint", "--config-dir", dir); code != 2 || !strings.Contains(e, "already exists") {
		t.Fatalf("duplicate = %d %q", code, e)
	}
	if _, out, _ := runBundlesCLI(t, "list", "--config-dir", dir); !strings.Contains(out, "lint: go-lint, shellcheck") {
		t.Errorf("list = %q", out)
	}
	if _, out, _ := runBundlesCLI(t, "show", "lint", "--config-dir", dir); !strings.Contains(out, "go-lint") {
		t.Errorf("show = %q", out)
	}
}

// TestBundlesDelete: --yes gate, delete and reload after deletion.
func TestBundlesDelete(t *testing.T) {
	dir := bundleConfig(t)
	if code, _, _ := runBundlesCLI(t, "create", "lint", "go-lint", "--config-dir", dir); code != 0 {
		t.Fatal("create failed")
	}
	if code, _, _ := runBundlesCLI(t, "delete", "lint", "--config-dir", dir); code != 2 {
		t.Errorf("delete without --yes = %d, want 2", code)
	}
	if code, out, _ := runBundlesCLI(t, "delete", "lint", "--yes", "--config-dir", dir); code != 0 || !strings.Contains(out, "deleted lint") {
		t.Errorf("delete = %d %q", code, out)
	}
	if code, out, _ := runBundlesCLI(t, "reload", "--config-dir", dir); code != 0 || !strings.Contains(out, "loaded 0 bundles") {
		t.Errorf("reload = %d %q", code, out)
	}
}
