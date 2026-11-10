package builtin

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestF8PLUG017DiskCleanup: the plugin defaults to a dry run, refuses a root
// outside the allowlist before opening it, and only removes after an explicit
// confirm.
func TestF8PLUG017DiskCleanup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	old := filepath.Join(root, "ephemeral.log")
	if err := os.WriteFile(old, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(Targets{}, DiskCleanupID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unwired target err = %v, want ErrUnavailable", err)
	}
	opts := DefaultCleanupOptions(root)
	rep, err := Cleanup(t.Context(), &opts)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if rep.Removed != 0 {
		t.Fatalf("dry run removed %d files", rep.Removed)
	}
	if _, statErr := os.Stat(old); statErr != nil {
		t.Fatalf("dry run deleted the file: %v", statErr)
	}

	foreign := DefaultCleanupOptions(filepath.Join(root, "..", "elsewhere"))
	foreign.AllowedRoots = []string{root}
	if _, ferr := Cleanup(t.Context(), &foreign); !errors.Is(ferr, ErrPath) {
		t.Fatalf("foreign root err = %v, want ErrPath", ferr)
	}
	opts.Confirm = true
	rep, err = Cleanup(t.Context(), &opts)
	if err != nil {
		t.Fatalf("confirmed cleanup: %v", err)
	}
	if rep.Removed != 1 {
		t.Fatalf("confirmed cleanup removed %d, want 1", rep.Removed)
	}
	if _, statErr := os.Stat(old); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("file still exists: %v", statErr)
	}
}
