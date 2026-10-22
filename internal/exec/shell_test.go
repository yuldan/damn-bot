package exec

import "testing"

// TestPowerShellArgv (SEC10-32): the one-shot argv carries the same
// execution-policy bypass as persistent PowerShell sessions, so a skill
// script can actually run when the gate allows it.
func TestPowerShellArgv(t *testing.T) {
	t.Parallel()
	got := shellChoice{bin: "powershell", kind: ShellPowerShell}.commandArgv("& 'x.ps1'")
	want := []string{"powershell", "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "& 'x.ps1'"}
	if len(got) != len(want) {
		t.Fatalf("argv = %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
