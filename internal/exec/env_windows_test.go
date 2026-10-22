package exec

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestLookPathWindows (ADR-088): .bat/.cmd never resolve (CreateProcess
// would run them through cmd.exe), relative PATH entries are skipped.
func TestLookPathWindows(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, n := range []string{"tool.bat", "tool.cmd"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("@echo off\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if p, err := lookPath("tool", dir, dir); !errors.Is(err, ErrNotFound) {
		t.Errorf("batch file resolved: %q", p)
	}
	sys32, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	p, err := lookPath("cmd", sys32, dir)
	if err != nil || !strings.EqualFold(filepath.Base(p), "cmd.exe") {
		t.Errorf("cmd: %q %v", p, err)
	}
	if _, err := lookPath("cmd", "System32", filepath.Dir(sys32)); err == nil {
		t.Error("relative PATH entry resolved")
	}
}

// TestEnvWindowsBasics: the variables a Win32 child needs are inherited.
func TestEnvWindowsBasics(t *testing.T) {
	t.Parallel()
	env := (&EnvPolicy{}).Env([]string{"SystemRoot=C:\\Windows", "Path=C:\\x", "AWS_SECRET=1"}, false, "", "", "")
	got := strings.Join(env, "\n")
	if !strings.Contains(got, "SystemRoot=C:\\Windows") || !strings.Contains(got, "Path=C:\\x") || strings.Contains(got, "AWS_SECRET") {
		t.Errorf("%v", env)
	}
	if envPath(env) != "C:\\x" {
		t.Errorf("case-insensitive PATH: %v", env)
	}
}

// TestNoDefaultCurrentDirectoryInExePath: a git.bat in the working directory
// must not run on `git status` (SEC10-26); cmd otherwise searches the cwd
// before PATH.
func TestNoDefaultCurrentDirectoryInExePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	if err := os.WriteFile(filepath.Join(dir, "git.bat"), []byte("@echo off\r\n> \""+marker+"\" echo ran\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, _, err := Prepare(&OneShot{Command: "git status", Dir: dir, Parent: os.Environ(), Policy: &EnvPolicy{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(spec.Env, "\n"), "NoDefaultCurrentDirectoryInExePath=1") {
		t.Fatalf("env missing NoDefaultCurrentDirectoryInExePath: %v", spec.Env)
	}
	r := New(t.Context())
	defer func() {
		if cerr := r.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	if _, rerr := r.Run(t.Context(), &spec); rerr != nil {
		t.Fatal(rerr)
	}
	if _, serr := os.Stat(marker); !os.IsNotExist(serr) {
		t.Fatalf("cwd git.bat ran: %v", serr)
	}
}

// negative control for SEC10-28: without the variable, cmd runs the cwd
// git.bat; proves the marker test can detect the issue.
func TestNoDefaultCurrentDirectoryNegativeControl(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	if err := os.WriteFile(filepath.Join(dir, "git.bat"), []byte("@echo off\r\n> \""+marker+"\" echo ran\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, _, err := Prepare(&OneShot{Command: "git status", Dir: dir, Parent: os.Environ(), Policy: &EnvPolicy{}})
	if err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, kv := range spec.Env {
		if !strings.HasPrefix(strings.ToLower(kv), "nodefaultcurrentdirectoryinexepath=") {
			env = append(env, kv)
		}
	}
	spec.Env = env
	r := New(t.Context())
	defer func() {
		if cerr := r.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	if _, rerr := r.Run(t.Context(), &spec); rerr != nil {
		t.Fatal(rerr)
	}
	if _, serr := os.Stat(marker); serr != nil {
		t.Fatalf("negative control: cwd git.bat did not run: %v", serr)
	}
}
