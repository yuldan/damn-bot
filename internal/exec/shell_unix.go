//go:build unix

package exec

import "fmt"

// chooseShell is the single Unix chooser for one-shot and persistent shells:
// bash, else sh. $SHELL is deliberately ignored so the execution shell always
// matches the Bash/POSIX classifier (SEC10-36); zsh is not selected.
func chooseShell(path string) (shellChoice, error) {
	for _, name := range []string{"bash", "sh"} {
		if p, err := lookPath(name, path, ""); err == nil {
			return shellChoice{bin: p, kind: ShellPOSIX}, nil
		}
	}
	return shellChoice{}, fmt.Errorf("%w: no shell on PATH", ErrNotFound)
}

func defaultShellKind() ShellKind { return ShellPOSIX }
