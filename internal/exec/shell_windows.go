//go:build windows

package exec

import "fmt"

// chooseShell is the single Windows chooser for one-shot and persistent
// shells: pwsh, powershell, else cmd (ADR-105 butir 22). The classifier
// dialect is derived from this, never guessed from runtime.GOOS (SEC10-29).
func chooseShell(path string) (shellChoice, error) {
	for _, name := range []string{"pwsh", "powershell"} {
		if p, err := lookPath(name, path, ""); err == nil {
			return shellChoice{bin: p, kind: ShellPowerShell}, nil
		}
	}
	if p, err := lookPath("cmd", path, ""); err == nil {
		return shellChoice{bin: p, kind: ShellCmd}, nil
	}
	return shellChoice{}, fmt.Errorf("%w: no shell on PATH", ErrNotFound)
}

func defaultShellKind() ShellKind { return ShellCmd }
