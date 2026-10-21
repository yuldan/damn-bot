//go:build windows

package journal

import "io/fs"

// oNoFollow is unavailable on Windows; os.Root already refuses symlinks
// that escape the root and the inode check is skipped (ADR-117).
const oNoFollow = 0

// syncDir is a no-op on Windows: directory handles cannot be flushed and
// NTFS journals directory metadata (ADR-094 butir 15).
func syncDir(string) error { return nil }

func statIdentity(fs.FileInfo) (dev, ino uint64) { return 0, 0 }

func hardLinks(fs.FileInfo) uint64 { return 1 }
