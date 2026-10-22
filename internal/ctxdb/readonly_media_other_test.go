//go:build !windows

package ctxdb

import (
	"os"
	"testing"
)

// makeDirReadOnly makes dir genuinely unwritable for this process and
// registers the restore.
func makeDirReadOnly(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // restore fixture
			t.Errorf("restore dir: %v", err)
		}
	})
}

func makeFileReadOnly(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Errorf("restore file: %v", err)
		}
	})
}
