//go:build unix

package exec

import (
	"os"
	"path/filepath"
	"testing"
)

// TestChooseShellSHELLZsh (SEC10-33): the Unix zsh session choice goes through
// chooseShell and maps to the POSIX/Bash dialect; zsh-specific syntax remains
// Unknown rather than auto-approved.
func TestChooseShellSHELLZsh(t *testing.T) {
	dir := t.TempDir()
	zsh := filepath.Join(dir, "zsh")
	if err := os.WriteFile(zsh, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(zsh, 0o700); err != nil { //nolint:gosec // test executable needs the exec bit
		t.Fatal(err)
	}
	t.Setenv("SHELL", zsh)
	c, err := chooseShell(os.Getenv("PATH"))
	if err != nil {
		t.Fatal(err)
	}
	if c.bin != zsh || c.kind != ShellPOSIX {
		t.Fatalf("chooseShell = %+v, want %q POSIX", c, zsh)
	}
}
