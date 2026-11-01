//go:build windows

package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGAPF5_TOOL04 is the Windows half of GAP-TOOL-04: even when bash.exe is
// on PATH, the shell chooser never selects it; the family is cmd when neither
// pwsh nor powershell exists, and the one-shot argv uses cmd /d /s /c.
func TestGAPF5_TOOL04(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"bash.exe", "cmd.exe"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"PATH=" + dir, "PATHEXT=.EXE"}
	if got := ShellKindForEnv(env); got != ShellCmd {
		t.Fatalf("shell kind with bash.exe on PATH = %d, want ShellCmd", got)
	}
	argv, err := shellArgv("echo hi", dir)
	if err != nil {
		t.Fatalf("shellArgv: %v", err)
	}
	if len(argv) < 4 || !strings.EqualFold(filepath.Base(argv[0]), "cmd.exe") {
		t.Fatalf("argv = %v, want cmd.exe", argv)
	}
	for _, arg := range argv {
		if arg == "/c" {
			return
		}
	}
	t.Fatalf("argv = %v, want /c", argv)
}
