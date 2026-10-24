//go:build unix

package sandbox

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// Open opens the immediate child name of d, read-only, without following a
// symlink and without blocking on a FIFO or device, and refuses a regular
// file with more than one link for approval (S16-7, CT27-4). It checks
// deny_paths by identity and lexical path only, so the codetools walkers
// pay no per-file readlink (SEC5-21).
func (d *Dir) Open(name string) (*os.File, Verdict, error) {
	f, err := d.openVetted(name, os.O_RDONLY, 0, false)
	if err != nil {
		return nil, Inside, err
	}
	if n, nerr := linkCountFile(f); nerr != nil {
		return nil, Inside, closeFail(f, nerr)
	} else if n > 1 {
		return nil, HardLinked, closeFail(f, nil) // nil unless Close fails
	}
	return f, Inside, nil
}

// OpenFile opens the immediate child name of d with flag: one
// openat(O_NOFOLLOW) from the pinned descriptor, vetted like Open with the
// handle path (lexical fallback) as well; a hard-linked file is returned
// and the caller decides (JN2-5/JN2-12).
func (d *Dir) OpenFile(name string, flag int, perm fs.FileMode) (*os.File, error) {
	return d.openVetted(name, flag, perm, true)
}

// OpenChild opens the immediate child name of d as a directory without
// following a symlink (one openat from the pinned descriptor), vetted like
// OpenFile so a deny_paths directory renamed under the parent is refused;
// "." opens a fresh descriptor for d itself. Callers walk a resolved
// relative path one component at a time so no path component can be
// redirected after Check (REVIEW-F2-SEC5-16b/19).
func (d *Dir) OpenChild(name string) (*Dir, error) {
	f, err := d.openVetted(name, unix.O_RDONLY|unix.O_DIRECTORY, 0, true)
	if err != nil {
		return nil, err
	}
	return &Dir{sb: d.sb, f: f, path: d.child(name)}, nil
}

func (d *Dir) openVetted(name string, flag int, perm fs.FileMode, byPath bool) (*os.File, error) {
	if name != "." {
		if err := dirChild(name); err != nil {
			return nil, err
		}
	}
	fd, err := openatEINTR(int(d.f.Fd()), name, flag|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, uint32(perm.Perm()))
	if err != nil {
		return nil, walkErr(d.child(name), err)
	}
	f, err := newFile(fd, d.child(name))
	if err != nil {
		return nil, err
	}
	return d.vetHandle(f, name, byPath)
}

// openatEINTR retries openat(2) when interrupted by a signal.
func openatEINTR(dfd int, name string, flags int, perm uint32) (int, error) {
	for {
		fd, err := unix.Openat(dfd, name, flags, perm)
		if !errors.Is(err, unix.EINTR) {
			return fd, err //nolint:wrapcheck // syscall cause kept for the sandbox error.
		}
	}
}

// Chmod applies mode with fchmod to the immediate child name of d, opened
// through the pinned descriptor without following a symlink: a link swapped
// in after the check never receives the mode (JN2-1).
func (d *Dir) Chmod(name string, mode fs.FileMode) error {
	f, _, err := d.Open(name)
	if err != nil {
		return err
	}
	if f == nil {
		return &fs.PathError{Op: "chmod", Path: d.child(name), Err: ErrNotRegular}
	}
	return errors.Join(f.Chmod(mode), f.Close())
}
