//go:build windows

package journal

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// replaceFile replaces dst with MoveFileEx(REPLACE_EXISTING|WRITE_THROUGH),
// the codetools rule (CODE-TOOLS §5.4).
func replaceFile(src, dst string) error {
	s, err := windows.UTF16PtrFromString(src)
	if err == nil {
		var d *uint16
		if d, err = windows.UTF16PtrFromString(dst); err == nil {
			err = windows.MoveFileEx(s, d, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		}
	}
	if err != nil {
		return fmt.Errorf("journal: MoveFileEx: %w", err)
	}
	return nil
}

// syncDir is a no-op on Windows: directory handles cannot be flushed and
// NTFS journals directory metadata (ADR-094 butir 15).
func syncDir(string) error { return nil }
