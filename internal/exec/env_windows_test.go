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

// gitBatSpec prepares `cmd /d /s /c "git status"` in dir. cmd is run
// directly (mode b) because it is the shell that searches the current
// directory for a bare name: mode (a) starts PowerShell, which never
// resolves a command from the cwd, so it cannot observe this variable at
// all (SEC10-28).
func gitBatSpec(t *testing.T, dir string) Spec {
	t.Helper()
	spec, _, err := Prepare(&OneShot{
		Direct: []string{"cmd", "/d", "/s", "/c", "git status"},
		Dir:    dir, Parent: os.Environ(), Policy: &EnvPolicy{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

// withoutKey returns env without the named variable (case-insensitive).
func withoutKey(env []string, name string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !strings.EqualFold(k, name) {
			out = append(out, kv)
		}
	}
	return out
}

// runSpec runs one prepared spec and returns the marker error.
func runSpec(t *testing.T, spec *Spec, marker string) error {
	t.Helper()
	r := New(t.Context())
	defer func() {
		if cerr := r.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	if _, rerr := r.Run(t.Context(), spec); rerr != nil {
		t.Fatal(rerr)
	}
	_, serr := os.Stat(marker)
	return serr
}

// TestNoDefaultCurrentDirectoryInExePath: a git.bat in the working directory
// must not run on `cmd /c git status` (SEC10-26); cmd otherwise searches the
// cwd before PATH.
func TestNoDefaultCurrentDirectoryInExePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	if err := os.WriteFile(filepath.Join(dir, "git.bat"), []byte("@echo off\r\n> \""+marker+"\" echo ran\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := gitBatSpec(t, dir)
	if !strings.Contains(strings.Join(spec.Env, "\n"), "NoDefaultCurrentDirectoryInExePath=1") {
		t.Fatalf("env missing NoDefaultCurrentDirectoryInExePath: %v", spec.Env)
	}
	if serr := runSpec(t, &spec, marker); !os.IsNotExist(serr) {
		t.Fatalf("cwd git.bat ran: %v", serr)
	}
}

// TestNoDefaultCurrentDirectoryNegativeControl (SEC10-28): the same cmd line
// with the variable removed does run the cwd git.bat, so the marker check
// above can detect a regression. Stripping it from a PowerShell one-shot
// would prove nothing: PowerShell ignores the cwd when resolving a command.
func TestNoDefaultCurrentDirectoryNegativeControl(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	if err := os.WriteFile(filepath.Join(dir, "git.bat"), []byte("@echo off\r\n> \""+marker+"\" echo ran\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := gitBatSpec(t, dir)
	spec.Env = withoutKey(spec.Env, "NoDefaultCurrentDirectoryInExePath")
	if serr := runSpec(t, &spec, marker); serr != nil {
		t.Fatalf("negative control: cwd git.bat did not run: %v", serr)
	}
}

// TestShellChoiceDrivesDialect (SEC10-29): chooseShell is the single source
// of truth for the one-shot and session interpreter and carries its family,
// so cmd/damnbot never guesses the classifier dialect from the OS: pwsh wins
// over powershell, which wins over cmd.
func TestShellChoiceDrivesDialect(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, n := range []string{"pwsh.exe", "powershell.exe", "cmd.exe"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		remove string
		want   ShellKind
		bin    string
	}{
		{"", ShellPowerShell, "pwsh.exe"},
		{"pwsh.exe", ShellPowerShell, "powershell.exe"},
		{"powershell.exe", ShellCmd, "cmd.exe"},
	} {
		if tc.remove != "" {
			if err := os.Remove(filepath.Join(dir, tc.remove)); err != nil {
				t.Fatal(err)
			}
		}
		c, err := chooseShell(dir)
		if err != nil || c.kind != tc.want || !strings.EqualFold(filepath.Base(c.bin), tc.bin) {
			t.Fatalf("chosen shell after removing %q = %q/%d (%v), want %s/%d", tc.remove, c.bin, c.kind, err, tc.bin, tc.want)
		}
		argv, err := shellArgv("echo hi", dir)
		if err != nil || !strings.EqualFold(filepath.Base(argv[0]), tc.bin) {
			t.Fatalf("one-shot argv after removing %q = %v (%v), want %s", tc.remove, argv, err, tc.bin)
		}
	}
}

// TestSessionResetPrefixes (SEC10-27): the PowerShell reset must end with
// `;` so a following line starting with `|` cannot attach to it; the cmd
// reset is a complete line.
func TestSessionResetPrefixes(t *testing.T) {
	t.Parallel()
	if ps := pwshSession("pwsh"); !strings.HasSuffix(ps.resetEnv, ";") || strings.Contains(ps.resetEnv, "\n") {
		t.Errorf("PowerShell resetEnv = %q", ps.resetEnv)
	}
	if sh := cmdSession("cmd.exe"); !strings.HasSuffix(sh.resetEnv, "\r\n") {
		t.Errorf("cmd resetEnv = %q", sh.resetEnv)
	}
}
