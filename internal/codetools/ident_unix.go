//go:build unix

package codetools

import (
	"os"
	"syscall"
)

// identify returns the device and inode of path (CT27-5). The caller has
// already resolved the path; Stat follows a remaining symlink, which is
// what makes symlink aliases share one identity. A missing syscall.Stat_t
// fails closed instead of falling back to a stat tuple.
func identify(path string) (fileID, error) {
	fi, err := os.Stat(path)
	if err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			return fileID{Vol: uint64(st.Dev), Lo: uint64(st.Ino)}, nil
		}
		err = syscall.EINVAL
	}
	return fileID{}, &IdentityError{Path: path, Err: err}
}
