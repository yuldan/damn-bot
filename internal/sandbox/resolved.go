package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Denied reports whether an absolute resolved path matches deny_paths,
// without any syscall. Walkers (grep/glob, ADR-103) use it to skip entries
// below a Check'ed directory, where an entry's lexical path is its
// resolved path because the walk never follows symlinks.
func (s *Sandbox) Denied(absResolved string) bool { return s.denied(absResolved) }

// RootOf returns the canonical root containing absResolved (a path Check
// returned) and absResolved relative to it. Callers that mutate files open
// this root and use the relative name, so an ancestor directory swapped
// for a symlink after Check cannot redirect the operation outside
// (REVIEW-F2-SEC5-12).
func (s *Sandbox) RootOf(absResolved string) (root, rel string, ok bool) {
	if !filepath.IsAbs(absResolved) {
		return "", "", false
	}
	for _, r := range s.roots {
		if hasPathPrefix(absResolved, r.path, s.foldRoot) {
			rel, err := filepath.Rel(r.path, absResolved)
			return r.path, rel, err == nil
		}
	}
	return "", "", false
}

// RootIdentity returns the FileInfo recorded for the root containing
// absResolved at New. A caller that pins the root handle compares it with
// os.SameFile so a root replaced by a symlink after Check is refused
// (REVIEW-F2-SEC5-16).
func (s *Sandbox) RootIdentity(absResolved string) (os.FileInfo, bool) {
	for _, r := range s.roots {
		if hasPathPrefix(absResolved, r.path, s.foldRoot) {
			return r.fi, true
		}
	}
	return nil, false
}

// PinRoot opens rootPath without following any symlink and verifies the
// handle is the root recorded at New, so a root replaced by a symlink after
// Check is refused (REVIEW-F2-SEC5-16).
func (s *Sandbox) PinRoot(ctx context.Context, rootPath string) (*Dir, error) {
	d, err := s.OpenDir(ctx, rootPath, false)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, ErrOutsideWorkspace
	}
	want, ok := s.RootIdentity(rootPath)
	fi, serr := d.File().Stat()
	if !ok || serr != nil || !os.SameFile(fi, want) {
		return nil, errors.Join(ErrOutsideWorkspace, d.Close(), serr)
	}
	return d, nil
}

// OpenResolved opens, read-only, a path a walker found below a Check'ed
// directory, skipping Check's per-component symlink resolution: the open
// itself refuses every symlink on the path (openat2 RESOLVE_NO_SYMLINKS,
// the O_NOFOLLOW walk with its post-open verification, or os.Root plus
// final-path verification on Windows), so a path that opens is its own
// resolved path. deny_paths wins (ErrDeniedPath), on the path and after
// the open on the handle identity (hard links to deny targets); a symlink
// anywhere on the path, including one swapped in after the walk listed
// it, fails with ErrOutsideWorkspace; a FIFO, socket or device fails,
// without blocking, with ErrNotRegular (CT27-4). Like OpenFile it returns
// (nil, verdict, nil) when the open needs approval: OutsideRoot outside
// every root, HardLinked for a regular file with more than one link,
// checked on the handle (S16-7). approved accepts both; the returned
// verdict is still the path's.
func (s *Sandbox) OpenResolved(ctx context.Context, absResolved string, approved bool) (*os.File, Verdict, error) {
	if err := ctx.Err(); err != nil {
		return nil, Inside, fmt.Errorf("sandbox: %w", err)
	}
	p := filepath.Clean(absResolved)
	if !filepath.IsAbs(p) {
		return nil, Inside, ErrInvalidPath
	}
	if s.denied(p) {
		return nil, Inside, ErrDeniedPath
	}
	// Outside every root the open starts at the volume root, so no ancestor
	// can be swapped for a symlink either (CT27-1).
	dir, v := filepath.VolumeName(p)+string(filepath.Separator), OutsideRoot
	for _, r := range s.roots {
		if hasPathPrefix(p, r.path, s.foldRoot) {
			dir, v = r.path, Inside
			break
		}
	}
	if v == OutsideRoot && !approved {
		return nil, OutsideRoot, nil
	}
	f, err := s.openResolved(dir, relTo(dir, p), p)
	// Inside a root the handle's path needs no re-check (see vet); outside,
	// the full post-open deny re-check of OpenApproved runs too (CT27-1).
	return s.vet(f, err, v, v == OutsideRoot, v == Inside && !approved)
}
