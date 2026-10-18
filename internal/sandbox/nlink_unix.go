//go:build unix

package sandbox

import (
	"fmt"
	"os"

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

// linkCountFile is linkCountPath for an open handle (fstat st_nlink).
func linkCountFile(f *os.File) (uint64, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return 0, fmt.Errorf("sandbox: fstat: %w", err)
	}
	return regularLinks(&st)
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
