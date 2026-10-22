//go:build unix

package journal

import (
	"context"
	"errors"
	"fmt"
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

// chmodFile applies mode to resolved, a path the sandbox checked, with
// fchmod on the verified handle: a symlink swapped in after the check never
// receives the mode (JN2-1).
func chmodFile(ctx context.Context, sb *sandbox.Sandbox, resolved string, mode fs.FileMode) error {
	f, _, err := sb.OpenResolved(ctx, resolved, true)
	if err != nil {
		return fmt.Errorf("journal: mode %s: %w", resolved, err)
	}
	if f == nil {
		return fmt.Errorf("journal: mode %s: %w", resolved, sandbox.ErrNotRegular)
	}
	return errors.Join(f.Chmod(mode), f.Close())
}
