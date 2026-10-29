//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package cron

// diskFree is unavailable on this platform; the low-water check fails open and
// the OS still surfaces ENOSPC at write time.
func diskFree(string) (free, total int64, err error) { return 0, 0, nil }
