//go:build !windows

package update

import (
	"fmt"
	"os"
)

// renameWithSharingWait renames once off Windows: no sharing-violation class
// exists there, so the central retry policy is not consulted.
func renameWithSharingWait(oldpath, newpath string) error {
	if err := os.Rename(oldpath, newpath); err != nil {
		return fmt.Errorf("rename %s: %w", oldpath, err)
	}
	return nil
}
