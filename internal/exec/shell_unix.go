//go:build unix

package exec

import (
	"fmt"
	"os"
	"path/filepath"
)

// chooseShell is the single Unix chooser for one-shot and persistent shells:
// $SHELL (bash/zsh), else bash, else sh. All are POSIX, so the classifier
// dialect is unaffected (zsh-specific syntax stays Unknown).
func chooseShell(path string) (shellChoice, error) {
	// $SHELL is how the persistent session has always selected zsh; route it
	// through the same chooser so one-shot and sessions agree (SEC10-33).
	if sh := os.Getenv("SHELL"); sh != "" {
		switch filepath.Base(sh) {
		case "bash", "zsh":
			if filepath.IsAbs(sh) && executable(sh) {
				return shellChoice{bin: sh, kind: ShellPOSIX}, nil
			}
		}
	}
	for _, name := range []string{"bash", "sh"} {
		if p, err := lookPath(name, path, ""); err == nil {
			return shellChoice{bin: p, kind: ShellPOSIX}, nil
		}
	}
	return shellChoice{}, fmt.Errorf("%w: no shell on PATH", ErrNotFound)
}

func defaultShellKind() ShellKind { return ShellPOSIX }
