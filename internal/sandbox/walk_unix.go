//go:build unix

package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// walkOpen opens dir/rel one component at a time with openat(O_NOFOLLOW)
// relative to the previous directory fd: equivalent to openat2
// RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS for a resolved rel without "..".
// Any symlink swapped in after Check fails with ErrOutsideWorkspace.
func walkOpen(dir, rel, want string, flag int, perm fs.FileMode) (*os.File, error) {
	dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: dir, Err: err}
	}
	comps := strings.Split(rel, "/")
	for _, c := range comps[:len(comps)-1] {
		next, oerr := unix.Openat(dfd, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(oerr, unix.ENOTDIR) && isSymlinkAt(dfd, c) {
			oerr = unix.ELOOP // Linux reports O_DIRECTORY before O_NOFOLLOW
		}
		closeDir(fdCloser(dfd))
		if oerr != nil {
			return nil, walkErr(want, oerr)
		}
		dfd = next
	}
	fd, err := unix.Openat(dfd, comps[len(comps)-1], flag|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm.Perm()))
	closeDir(fdCloser(dfd))
	if err != nil {
		return nil, walkErr(want, err)
	}
	return os.NewFile(uintptr(fd), want), nil
}

// walkErr maps a symlink refusal (ELOOP; EMLINK on FreeBSD/NetBSD) to
// ErrOutsideWorkspace and keeps any other error as *fs.PathError.
func walkErr(want string, err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EMLINK) {
		return escaped(want, err)
	}
	return &fs.PathError{Op: "openat", Path: want, Err: err}
}

func isSymlinkAt(dfd int, name string) bool {
	var st unix.Stat_t
	return unix.Fstatat(dfd, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil &&
		st.Mode&unix.S_IFMT == unix.S_IFLNK
}

// verifyInode is the post-open check where the kernel cannot report the
// fd's path (macOS/BSD: x/sys has no F_GETPATH wrapper and unsafe is
// banned, E-38; ADR-097): want, the resolved path, is looked up again
// without following its last component and must name the object f refers
// to (same device and inode). A directory of the walk renamed away or a
// component replaced meanwhile makes it name another object or none, and
// the open fails with ErrOutsideWorkspace. A rename that is undone before
// this lookup is not detected (ADR-097 residual risk); the O_NOFOLLOW walk
// still never traverses a symlink.
func verifyInode(f *os.File, want string) (*os.File, error) {
	var got, at unix.Stat_t
	err := unix.Fstat(int(f.Fd()), &got)
	if err == nil {
		err = unix.Lstat(want, &at)
	}
	if err == nil && got.Dev == at.Dev && got.Ino == at.Ino {
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

// fdCloser adapts a raw directory fd to io.Closer for closeDir.
type fdCloser int

func (f fdCloser) Close() error {
	if err := unix.Close(int(f)); err != nil {
		return fmt.Errorf("sandbox: close: %w", err)
	}
	return nil
}
