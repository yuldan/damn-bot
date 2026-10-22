package main

import (
	"runtime"
	"testing"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/exec/classify"
)

// TestRuntimeDialectFromShellChoice (SEC10-29): the dialect is derived from
// the shell exec chose, not from the OS; PowerShell must use the Pwsh lexer.
func TestRuntimeDialectFromShellChoice(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind exec.ShellKind
		want classify.Dialect
	}{
		{exec.ShellPOSIX, classify.Bash},
		{exec.ShellPowerShell, classify.Pwsh},
		{exec.ShellCmd, classify.Cmd},
	} {
		if got := dialectFor(tc.kind); got != tc.want {
			t.Errorf("dialectFor(%d) = %v, want %v", tc.kind, got, tc.want)
		}
	}
	got := runtimeDialect()
	if runtime.GOOS == "windows" {
		if got != classify.Pwsh && got != classify.Cmd {
			t.Errorf("runtimeDialect = %v, want Pwsh or Cmd", got)
		}
	} else if got != classify.Bash {
		t.Errorf("runtimeDialect = %v, want Bash", got)
	}
	if want := dialectFor(exec.RuntimeShellKind()); got != want {
		t.Errorf("runtimeDialect = %v, want %v from exec", got, want)
	}
}
