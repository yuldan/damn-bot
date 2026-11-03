//go:build !windows

package stt

import (
	"testing"

	"golang.org/x/sys/unix"
)

// makeExec marks a fixture executable without os.Chmod's G302 finding.
func makeExec(t *testing.T, path string) {
	t.Helper()
	if err := unix.Chmod(path, 0o700); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}
