//go:build unix

package journal

import (
	"errors"
	"io/fs"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/damnbot/damnbot/internal/sandbox"
)

// oNoFollow refuses a symlink in the final component of an openat; the open
// itself is sandbox.Dir.OpenFile, which also vets deny_paths (SEC5-19).
const oNoFollow = syscall.O_NOFOLLOW

// removeChild unlinks name from the pinned parent directory h.
func removeChild(h *sandbox.Dir, name string) error {
	return unlinkatEINTR(int(h.File().Fd()), name)
}

// renameChild moves name in h to newName in dst, both pinned descriptors.
func renameChild(h *sandbox.Dir, name string, dst *sandbox.Dir, newName string) error {
	return renameatEINTR(int(h.File().Fd()), name, int(dst.File().Fd()), newName)
}

// syncDirHandle makes a create/rename/remove in h durable (POSIX).
func syncDirHandle(h *sandbox.Dir) error {
	return fsyncEINTR(int(h.File().Fd()))
}

func unlinkatEINTR(fd int, name string) error {
	for {
		if err := unix.Unlinkat(fd, name, 0); !errors.Is(err, unix.EINTR) {
			return err //nolint:wrapcheck // syscall cause kept for the journal error.
		}
	}
}

func renameatEINTR(oldfd int, oldName string, newfd int, newName string) error {
	for {
		if err := unix.Renameat(oldfd, oldName, newfd, newName); !errors.Is(err, unix.EINTR) {
			return err //nolint:wrapcheck // syscall cause kept for the journal error.
		}
	}
}

func fsyncEINTR(fd int) error {
	for {
		if err := unix.Fsync(fd); !errors.Is(err, unix.EINTR) {
			return err //nolint:wrapcheck // syscall cause kept for the journal error.
		}
	}
}

func hardLinks(fi fs.FileInfo) uint64 {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 1
	}
	return uint64(st.Nlink)
}

// makeReplaceable is a no-op on POSIX: rename replaces the directory entry
// regardless of the destination mode.
func makeReplaceable(*sandbox.Dir, string) error { return nil }
