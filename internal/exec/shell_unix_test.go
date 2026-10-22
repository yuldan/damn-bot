//go:build unix

package exec

import (
	"os"
	"path/filepath"
	"testing"
)

// TestChooseShellIgnoresSHELL (SEC10-36): $SHELL=zsh must not drive the
// choice; chooseShell stays bash -> sh so execution matches the Bash dialect.
func TestChooseShellIgnoresSHELL(t *testing.T) {
	dir := t.TempDir()
	bash := filepath.Join(dir, "bash")
	zsh := filepath.Join(dir, "zsh")
	for _, p := range []string{bash, zsh} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o700); err != nil { //nolint:gosec // test executable needs the exec bit
			t.Fatal(err)
		}
	}
	t.Setenv("SHELL", zsh)
	c, err := chooseShell(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.bin != bash || c.kind != ShellPOSIX {
		t.Fatalf("chooseShell = %+v, want %q POSIX", c, bash)
	}
}
