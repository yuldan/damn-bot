//go:build unix

package sandbox

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// linkCountPath is the link count of the regular file at p (not followed:
// p is resolved); 1 for a directory, ErrNotRegular for any other object
// (S16-7, CT27-4).
func linkCountPath(p string) (uint64, error) {
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		return 0, fmt.Errorf("sandbox: lstat: %w", err)
	}
	return regularLinks(&st)
}

// linkCountFile is linkCountPath for an open handle, from its stat fi
// (st_nlink); f is unused on Unix.
func linkCountFile(_ *os.File, fi os.FileInfo) (uint64, error) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && fi.Mode().IsRegular() {
		return uint64(st.Nlink), nil
	}
	if fi.IsDir() {
		return 1, nil
	}
	return 0, ErrNotRegular
}

func regularLinks(st *unix.Stat_t) (uint64, error) {
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		return uint64(st.Nlink), nil
	case unix.S_IFDIR:
		return 1, nil
	}
	return 0, ErrNotRegular
}
