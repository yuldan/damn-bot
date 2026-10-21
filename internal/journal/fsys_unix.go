//go:build !windows

package journal

import (
	"errors"
	"fmt"
	"os"
)

// replaceFile atomically replaces dst with src.
func replaceFile(src, dst string) error {
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("journal: rename: %w", err)
	}
	return nil
}

// syncDir makes a rename/remove durable (POSIX). os.Root keeps the path
// relative to the sandbox-resolved directory (gosec G304).
func syncDir(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("journal: open dir: %w", err)
	}
	f, err := root.Open(".")
	if err != nil {
		return errors.Join(fmt.Errorf("journal: open dir: %w", err), root.Close())
	}
	return errors.Join(f.Sync(), f.Close(), root.Close())
}
