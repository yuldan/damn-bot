package sandbox

import (
	"errors"
	"io/fs"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// openat2Retries bounds EAGAIN retries: the kernel returns EAGAIN when a
// concurrent rename/mount may have raced a restricted lookup.
const openat2Retries = 16

func (s *Sandbox) openBeneath(dir, rel, want string, flag int, perm fs.FileMode) (*os.File, error) {
	if !s.noOpenat2 {
		f, err := openat2(want, flag, perm)
		// ENOSYS: kernel < 5.6; EPERM: seccomp filter (containers).
		if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EPERM) {
			return f, err
		}
	}
	f, err := walkOpen(dir, rel, want, flag, perm)
	if err != nil {
		return nil, err
	}
	return verifyProcFd(f, want)
}

// openat2 opens the clean absolute path want (lexically below the
// canonical dir) with one openat2 RESOLVE_NO_SYMLINKS lookup from "/":
// with no symlink allowed on any component, the opened object is exactly
// the lexical path, so the result equals openat2 RESOLVE_BENEATH from dir
// without opening and closing dir first (ADR-103). The last component is
// opened O_NONBLOCK and vetted by newFile (CT27-4); EAGAIN/EINTR retry.
func openat2(want string, flag int, perm fs.FileMode) (*os.File, error) {
	how := unix.OpenHow{
		Flags:   uint64(flag) | unix.O_CLOEXEC | unix.O_NONBLOCK, //nolint:gosec // flag is a non-negative open(2) bitmask.
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	}
	if flag&(os.O_CREATE|unix.O_TMPFILE) != 0 {
		how.Mode = uint64(perm.Perm())
	}
	for range openat2Retries {
		fd, err := unix.Openat2(unix.AT_FDCWD, want, &how)
		switch {
		case err == nil:
			return newFile(fd, flag, want)
		case errors.Is(err, unix.EAGAIN), errors.Is(err, unix.EINTR):
			continue
		default:
			return nil, walkErr(want, err)
		}
	}
	return nil, escaped(want, unix.EAGAIN)
}

// handlePath is the real path of f: readlink /proc/self/fd/N.
func handlePath(f *os.File) (string, bool) {
	p, err := os.Readlink("/proc/self/fd/" + strconv.FormatUint(uint64(f.Fd()), 10))
	return p, err == nil
}

// verifyProcFd checks the real path of f against want (CODE-TOOLS §8:
// readlink /proc/self/fd/N); failure, including no /proc, fails closed.
func verifyProcFd(f *os.File, want string) (*os.File, error) {
	if got, ok := handlePath(f); ok && got == want {
		return f, nil
	}
	return nil, closeFail(f, escaped(want, fs.ErrInvalid))
}
