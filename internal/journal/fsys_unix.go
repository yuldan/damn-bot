//go:build !windows

package journal

import (
	"errors"
	"os"
)

// replaceFile atomically replaces dst with src.
func replaceFile(src, dst string) error { return os.Rename(src, dst) }

// syncDir makes a rename/remove durable (POSIX).
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
