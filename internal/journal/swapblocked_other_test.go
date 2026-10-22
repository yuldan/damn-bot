//go:build !windows

package journal

// swapBlocked reports whether a rename failed because the directory still
// has an open handle. POSIX renames over an open handle, so any failure of
// the probe is a real failure there.
func swapBlocked(error) bool { return false }
