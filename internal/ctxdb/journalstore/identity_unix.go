//go:build unix

package journalstore

import (
	"io/fs"
	"syscall"
)

// statIdentity returns device/inode for an undo identity check.
func statIdentity(fi fs.FileInfo) (dev, ino uint64) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	return uint64(st.Dev), uint64(st.Ino)
}
