//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package doctor

import (
	"fmt"
	"math"
	"syscall"
)

// diskFree reports free and total bytes of the filesystem holding path.
func diskFree(path string) (free, total int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	if st.Bavail > math.MaxInt64 || st.Blocks > math.MaxInt64 {
		return 0, 0, fmt.Errorf("statfs %s: block count overflows int64", path)
	}
	return mulSize(int64(st.Bavail), int64(st.Bsize)), mulSize(int64(st.Blocks), int64(st.Bsize)), nil
}

// mulSize multiplies positive byte counts without overflowing int64.
func mulSize(a, b int64) int64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	if a > math.MaxInt64/b {
		return math.MaxInt64
	}
	return a * b
}
