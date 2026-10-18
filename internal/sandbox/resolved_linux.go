package sandbox

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// openResolved opens the clean absolute path want (lexically below the
// canonical dir) with one openat2 RESOLVE_NO_SYMLINKS lookup from "/":
// with no symlink allowed on any component, the opened object is exactly
// the lexical path, so the result equals openat2 RESOLVE_BENEATH from dir
// without opening dir first. Kernels without openat2 fall back to
// openBeneath.
func (s *Sandbox) openResolved(dir, rel, want string) (*os.File, error) {
	if !s.noOpenat2 {
		f, err := openat2(unix.AT_FDCWD, want, want, &unix.OpenHow{
			Flags:   unix.O_RDONLY,
			Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
		})
		// ENOSYS: kernel < 5.6; EPERM: seccomp filter (containers).
		if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EPERM) {
			return f, err
		}
	}
	return s.openBeneath(dir, rel, want, os.O_RDONLY, 0)
}
