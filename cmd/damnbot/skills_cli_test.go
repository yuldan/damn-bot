package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readTrustStore reads the store through os.OpenRoot, so the test never
// follows a path outside the temp config directory.
func readTrustStore(t *testing.T, configDir string) string {
	t.Helper()
	root, err := os.OpenRoot(configDir)
	if err != nil {
		t.Fatal(err)
	}
	b, rerr := root.ReadFile("trusted-projects")
	if cerr := root.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	if rerr != nil && !os.IsNotExist(rerr) {
		t.Fatal(rerr)
	}
	return string(b)
}

// trustEnv points the CLI at a temp config dir and returns it. The directory
// is resolved through the production loader, not assumed to be <home>/damnbot:
// darwin uses ~/Library/Application Support and windows AppData, so a hardcoded
// path only works on linux.
func trustEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_DATA_HOME", home)
	dir, err := skillsConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestSkillsTrustCLI (CLI-046, ADR-175): add/list/remove manage
// <config>/trusted-projects, normalise the path, are idempotent, and reject a
// path that is not an existing directory.
func TestSkillsTrustCLI(t *testing.T) {
	configDir := trustEnv(t) // t.Setenv tidak kompatibel dengan t.Parallel()
	project := t.TempDir()
	var out, errb bytes.Buffer
	if code := skillsCommand([]string{"trust", "add", project}, &out, &errb); code != 0 {
		t.Fatalf("add = %d, stderr %s", code, errb.String())
	}
	want, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	if body := readTrustStore(t, configDir); body != want+"\n" {
		t.Fatalf("trusted-projects = %q, want %q", body, want+"\n")
	}
	// Idempotent: adding the same project twice keeps one line.
	out.Reset()
	if code := skillsCommand([]string{"trust", "add", project}, &out, &errb); code != 0 {
		t.Fatalf("second add = %d", code)
	}
	if body := readTrustStore(t, configDir); strings.Count(body, "\n") != 1 {
		t.Fatalf("duplicate entry: %q", body)
	}
	// list is read-only and prints the canonical path.
	out.Reset()
	if code := skillsCommand([]string{"trust", "list"}, &out, &errb); code != 0 || out.String() != want+"\n" {
		t.Fatalf("list = %d %q", code, out.String())
	}
	// remove takes it out again; a second remove is a clear error.
	out.Reset()
	errb.Reset()
	if code := skillsCommand([]string{"trust", "remove", project}, &out, &errb); code != 0 {
		t.Fatalf("remove = %d %s", code, errb.String())
	}
	if body := readTrustStore(t, configDir); body != "" {
		t.Fatalf("store not empty: %q", body)
	}
	if code := skillsCommand([]string{"trust", "remove", project}, &out, &errb); code != 1 {
		t.Fatalf("second remove = %d, want 1", code)
	}
}

// TestSkillsTrustCLIRejects covers the fail-closed cases: a file is not a
// project, an empty path and usage errors are refused before any write, so a
// typo can never make a playground trusted.
func TestSkillsTrustCLIRejects(t *testing.T) {
	configDir := trustEnv(t) // t.Setenv tidak kompatibel dengan t.Parallel()
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := skillsCommand([]string{"trust", "add", file}, &out, &errb); code != 1 {
		t.Fatalf("add file = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "bukan direktori") {
		t.Fatalf("stderr = %q", errb.String())
	}
	if code := skillsCommand([]string{"trust", "add", "  "}, &out, &errb); code != 1 {
		t.Fatalf("add blank = %d, want 1", code)
	}
	if code := skillsCommand([]string{"trust", "add", filepath.Join(t.TempDir(), "missing")}, &out, &errb); code != 1 {
		t.Fatalf("add missing = %d, want 1", code)
	}
	if code := skillsCommand([]string{"trust"}, &out, &errb); code != 2 {
		t.Fatalf("usage = %d, want 2", code)
	}
	if code := skillsCommand([]string{"bogus"}, &out, &errb); code != 2 {
		t.Fatalf("unknown = %d, want 2", code)
	}
	if _, err := os.Stat(filepath.Join(configDir, "trusted-projects")); !os.IsNotExist(err) {
		t.Fatalf("rejected input still wrote the store: %v", err)
	}
}
