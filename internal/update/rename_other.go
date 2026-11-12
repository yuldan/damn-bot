//go:build !windows

package update

import (
	"fmt"
	"os"
)

// renameWithRetry renames once off Windows: no sharing-violation class exists.
func renameWithRetry(oldpath, newpath string) error {
	if err := os.Rename(oldpath, newpath); err != nil {
		return fmt.Errorf("rename %s: %w", oldpath, err)
	}
	return nil
}
