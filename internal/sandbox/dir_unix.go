//go:build unix

package sandbox

import (
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// Open opens the immediate child name of d, read-only, without following a
// symlink and without blocking on a FIFO or device: openat(O_NOFOLLOW|
// O_NONBLOCK) + one fstat vetting the type and the hard-link count. The
// semantics equal OpenResolved for a path below the directory d was
// validated at: deny_paths was checked by the caller on the full path, the
// handle identity check uses the same deny list, and a second link yields
// (nil, HardLinked, nil) for approval (S16-7, CT27-4).
func (d *Dir) Open(name string) (*os.File, Verdict, error) {
	if err := dirChild(name); err != nil {
		return nil, Inside, err
	}
	fd, err := unix.Openat(int(d.f.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, Inside, walkErr(d.child(name), err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		closeDir(fdCloser(fd))
		return nil, Inside, &fs.PathError{Op: "open", Path: d.child(name), Err: err}
	}
	if _, err := regularLinks(&st); err != nil {
		closeDir(fdCloser(fd))
		return nil, Inside, &fs.PathError{Op: "open", Path: d.child(name), Err: err}
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		closeDir(fdCloser(fd))
		return nil, Inside, &fs.PathError{Op: "open", Path: d.child(name), Err: err}
	}
	f := os.NewFile(uintptr(fd), name)
	if len(d.sb.denyIDs) > 0 {
		denied, derr := d.sb.sameDenyID(&st, f)
		if derr != nil {
			return nil, Inside, closeFail(f, &fs.PathError{Op: "open", Path: d.child(name), Err: derr})
		}
		if denied {
			return nil, Inside, closeFail(f, ErrDeniedPath)
		}
	}
	if st.Nlink > 1 {
		return nil, HardLinked, closeFail(f, nil)
	}
	return f, Inside, nil
}
