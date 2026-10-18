//go:build windows

package ctxdb

import "os"

// syncDirFile is a no-op on Windows: a directory handle opened for reading
// cannot be flushed (FlushFileBuffers needs GENERIC_WRITE, which directories
// refuse: "Access is denied"), and NTFS journals directory metadata itself.
// SQLite's Windows VFS likewise never syncs directories. File contents are
// still fsynced before every rename (ADR-094).
func syncDirFile(*os.File) error { return nil }
