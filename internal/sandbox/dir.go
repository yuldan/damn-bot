package sandbox

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"sync"
)

// Dir is an opened, validated directory: an immediate child is opened with
// openat(O_NOFOLLOW) relative to its descriptor, so a walker that already
// reached the directory pays one component lookup per file instead of a
// full path resolution (CT-16). The descriptor is the sandbox's only file
// access path for those children; deny_paths and the regular-file rule are
// still applied on every open. Close is safe to call more than once.
type Dir struct {
	sb   *Sandbox
	f    *os.File
	path string // resolved absolute path (fallbacks, diagnostics)
	once sync.Once
}

// OpenDir opens absResolved, a directory a walker reached below a Check'ed
// directory (or the root itself), and keeps the handle for relative opens.
// It applies the same rules as OpenResolved: deny_paths, no symlink on the
// path, regular directories only.
func (s *Sandbox) OpenDir(ctx context.Context, absResolved string, approved bool) (*Dir, error) {
	f, _, err := s.OpenResolved(ctx, absResolved, approved)
	if err != nil || f == nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, closeFail(f, fmt.Errorf("sandbox: stat: %w", err))
	}
	if !fi.IsDir() {
		return nil, closeFail(f, &fs.PathError{Op: "open", Path: absResolved, Err: ErrNotRegular})
	}
	return &Dir{sb: s, f: f, path: absResolved}, nil
}

// File returns the directory handle for ReadDir. It stays valid until
// Close; callers must not close it themselves.
func (d *Dir) File() *os.File { return d.f }

// Close releases the directory descriptor; later calls are no-ops.
func (d *Dir) Close() error {
	d.once.Do(func() { d.f.Close() }) //nolint:errcheck,gosec // lookup-only directory handle: close cannot lose data.
	return nil
}

// dirChild rejects a name that is not one immediate component: a
// separator, "." or ".." would leave the validated directory or reopen a
// path the walker never checked.
func dirChild(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("%w: %q", ErrInvalidPath, name)
	}
	for i := range len(name) {
		if os.IsPathSeparator(name[i]) {
			return fmt.Errorf("%w: %q", ErrInvalidPath, name)
		}
	}
	return nil
}

// child is the full path of name below d, built only for error reporting.
func (d *Dir) child(name string) string { return d.path + "/" + name }

// vetHandle vets an opened child handle: deny_paths by identity (vet), then
// by path. With byPath the handle's real path is used when the OS reports
// one, falling back to the lexical child path when it does not (SEC5-20);
// without byPath (the codetools walkers) only the lexical path is checked,
// so no per-file readlink is paid (SEC5-21).
func (d *Dir) vetHandle(f *os.File, name string, byPath bool) (*os.File, error) {
	vf, _, err := d.sb.vet(f, nil, Inside, false, false)
	if err != nil {
		return nil, err
	}
	p := d.child(name)
	if byPath {
		hp := handlePath
		if d.sb.handlePath != nil {
			hp = d.sb.handlePath
		}
		if got, ok := hp(vf); ok {
			p = got
		}
	}
	if d.sb.denied(p) {
		return nil, closeFail(vf, ErrDeniedPath)
	}
	return vf, nil
}
