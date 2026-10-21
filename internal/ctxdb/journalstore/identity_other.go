//go:build !unix

package journalstore

import "io/fs"

// statIdentity is unavailable without inode numbers (Windows); undo then
// relies on the O_NOFOLLOW open and the content hash (ADR-117).
func statIdentity(fs.FileInfo) (dev, ino uint64) { return 0, 0 }
