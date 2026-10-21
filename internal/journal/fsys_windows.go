//go:build windows

package journal

import (
	"errors"
	"io/fs"
	"os"
)

// oNoFollow is unavailable on Windows; os.Root already refuses symlinks
// that escape the root and the inode check is skipped (ADR-117).
const oNoFollow = 0

// syncDir is a no-op on Windows: directory handles cannot be flushed and
// NTFS journals directory metadata (ADR-094 butir 15).
func syncDir(string) error { return nil }

func hardLinks(fs.FileInfo) uint64 { return 1 }

// makeReplaceable clears the read-only attribute before a temp file replaces
// the destination: NTFS refuses rename over a read-only file.
func makeReplaceable(root *os.Root, base string) error {
	if err := root.Chmod(base, 0o600); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
