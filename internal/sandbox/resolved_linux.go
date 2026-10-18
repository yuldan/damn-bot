package sandbox

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// openResolved opens the clean absolute path want (lexically below the
// canonical root dir) with one openat2 RESOLVE_NO_SYMLINKS lookup from "/":
// with no symlink allowed on any component, the opened object is exactly
// the lexical path, so the result equals openat2 RESOLVE_BENEATH from the
// root without opening the root first. Kernels without openat2 fall back
// to openBeneath.
func (s *Sandbox) openResolved(dir, rel, want string) (*os.File, error) {
	if s.noOpenat2 {
		return s.openBeneath(dir, rel, want, os.O_RDONLY, 0)
	}
	how := unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	}
	for range openat2Retries {
		fd, err := unix.Openat2(unix.AT_FDCWD, want, &how)
		switch {
		case err == nil:
			return os.NewFile(uintptr(fd), want), nil
		case errors.Is(err, unix.EAGAIN), errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EPERM):
			return s.openBeneath(dir, rel, want, os.O_RDONLY, 0)
		case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
			return nil, escaped(want, err)
		default:
			return nil, &fs.PathError{Op: "openat2", Path: want, Err: err}
		}
	}
	return nil, escaped(want, unix.EAGAIN)
}
