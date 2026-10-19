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
	env := (&EnvPolicy{}).Env([]string{"SystemRoot=C:\\Windows", "Path=C:\\x", "AWS_SECRET=1"}, false, "")
	got := strings.Join(env, "\n")
	if !strings.Contains(got, "SystemRoot=C:\\Windows") || !strings.Contains(got, "Path=C:\\x") || strings.Contains(got, "AWS_SECRET") {
		t.Errorf("%v", env)
	}
	if envPath(env) != "C:\\x" {
		t.Errorf("case-insensitive PATH: %v", env)
	}
}
