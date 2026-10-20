//go:build windows

package tools

// syncDir is a no-op on Windows: directories have no fsync equivalent and
// MoveFileEx with WRITE_THROUGH is used for the file itself.
func syncDir(string) error { return nil }
