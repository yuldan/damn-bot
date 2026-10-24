//go:build !unix

package sandbox

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
)

// Open on platforms without openat falls back to OpenResolved on the full
// path: same sandbox rules, just without the one-component fast path.
func (d *Dir) Open(name string) (*os.File, Verdict, error) {
	if err := dirChild(name); err != nil {
		return nil, Inside, err
	}
	return d.sb.OpenResolved(context.Background(), filepath.Join(d.path, name), false)
}

// OpenFile opens the immediate child name of d through an os.Root of d, so
// os.Root refuses escapes, then vets the handle (identity plus handle path
// with a lexical fallback, SEC5-19/20). Symlinks that stay inside the root
// are followed on these platforms (static residual).
func (d *Dir) OpenFile(name string, flag int, perm fs.FileMode) (*os.File, error) {
	if err := dirChild(name); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(d.path)
	if err != nil {
		return nil, err
	}
	defer closeDir(r)
	f, err := r.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return d.vetHandle(f, name, true)
}

// OpenChild opens the immediate child name of d as a directory; "." reopens
// d. These platforms have no openat, so the resolved path is reopened
// through OpenDir (deny- and symlink-checked; static residual).
func (d *Dir) OpenChild(name string) (*Dir, error) {
	if name != "." {
		if err := dirChild(name); err != nil {
			return nil, err
		}
	}
	return d.sb.OpenDir(context.Background(), filepath.Join(d.path, name), false)
}
