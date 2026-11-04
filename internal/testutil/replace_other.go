//go:build !windows

package testutil

import (
	"fmt"
	"os"
)

// replaceFile installs tmp as dst. Unix rename atomically replaces dst without
// exposing a partially written file.
func replaceFile(tmp, dst string) error {
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return nil
}
