//go:build unix

package journal

import (
	"context"
	"io/fs"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/damnbot/damnbot/internal/sandbox"
)

// removeChild unlinks name from the pinned parent directory h.
func removeChild(h *sandbox.Dir, name string) error {
	return unix.Unlinkat(int(h.File().Fd()), name, 0) //nolint:wrapcheck // syscall cause kept for the journal error.
}

// renameChild moves name in h to newName in dst, both pinned descriptors.
func renameChild(h *sandbox.Dir, name string, dst *sandbox.Dir, newName string) error {
	return unix.Renameat(int(h.File().Fd()), name, int(dst.File().Fd()), newName) //nolint:wrapcheck // syscall cause kept for the journal error.
}

// syncDirHandle makes a create/rename/remove in h durable (POSIX).
func syncDirHandle(h *sandbox.Dir) error {
	return unix.Fsync(int(h.File().Fd())) //nolint:wrapcheck // syscall cause kept for the journal error.
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
func makeReplaceable(context.Context, *sandbox.Sandbox, string) error { return nil }
