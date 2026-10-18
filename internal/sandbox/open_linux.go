package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// openat2Retries bounds EAGAIN retries: with RESOLVE_BENEATH the kernel
// returns EAGAIN when a concurrent rename/mount may have raced the lookup.
const openat2Retries = 16

func (s *Sandbox) openBeneath(dir, rel, want string, flag int, perm fs.FileMode) (*os.File, error) {
	if !s.noOpenat2 {
		f, err := openat2Beneath(dir, rel, want, flag, perm)
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

func openat2Beneath(dir, rel, want string, flag int, perm fs.FileMode) (*os.File, error) {
	dfd, err := unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: dir, Err: err}
	}
	defer closeDir(fdCloser(dfd))
	how := unix.OpenHow{
		Flags: uint64(flag) | unix.O_CLOEXEC, //nolint:gosec // flag is a non-negative open(2) bitmask.
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS |
			unix.RESOLVE_NO_MAGICLINKS,
	}
	if flag&(os.O_CREATE|unix.O_TMPFILE) != 0 {
		how.Mode = uint64(perm.Perm())
	}
	for range openat2Retries {
		fd, err := unix.Openat2(dfd, rel, &how)
		switch {
		case err == nil:
			return os.NewFile(uintptr(fd), want), nil
		case errors.Is(err, unix.EAGAIN), errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EXDEV), errors.Is(err, unix.ELOOP):
			return nil, escaped(want, err)
		default:
			return nil, &fs.PathError{Op: "openat2", Path: want, Err: err}
		}
	}
	return nil, escaped(want, unix.EAGAIN)
}

// handlePath is the real path of f: readlink /proc/self/fd/N.
func handlePath(f *os.File) (string, bool) {
	p, err := fdPath(f)
	return p, err == nil
}

func fdPath(f *os.File) (string, error) {
	p, err := os.Readlink("/proc/self/fd/" + strconv.FormatUint(uint64(f.Fd()), 10))
	if err != nil {
		return "", fmt.Errorf("sandbox: fd path: %w", err)
	}
	return p, nil
}

// verifyProcFd checks the real path of f against want (CODE-TOOLS §8:
// readlink /proc/self/fd/N); failure, including no /proc, fails closed.
func verifyProcFd(f *os.File, want string) (*os.File, error) {
	got, err := fdPath(f)
	if err == nil && got == want {
		return f, nil
	}
	if cerr := f.Close(); cerr != nil {
		return nil, fmt.Errorf("sandbox: close: %w", cerr)
	}
	if err == nil {
		err = fs.ErrInvalid
	}
	return nil, escaped(want, err)
}
