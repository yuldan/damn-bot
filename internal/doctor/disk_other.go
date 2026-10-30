//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package doctor

// diskFree is unavailable here; the disk check reports the sizes it can read
// and leaves the free-space verdict to the write path (ENOSPC).
func diskFree(string) (free, total int64, err error) { return 0, 0, nil }
