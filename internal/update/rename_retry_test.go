package update

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRetryRename pins the rename surface after its loop moved into the central
// netx/retry policy: a successful rename, and a missing source that must not be
// retried. The transient/permanent split and the attempt bound are covered by
// TestRetryLocalPolicy in internal/netx/retry.
func TestRetryRename(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := renameWithSharingWait(src, dst); err != nil {
		t.Fatalf("rename = %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("destination: %v", err)
	}
	if err := renameWithSharingWait(src, dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing source = %v, want ErrNotExist", err)
	}
}
