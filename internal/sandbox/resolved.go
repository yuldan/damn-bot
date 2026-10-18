package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// Denied reports whether an absolute resolved path matches deny_paths,
// without any syscall. Walkers (grep/glob, ADR-103) use it to skip entries
// below a Check'ed directory, where an entry's lexical path is its
// resolved path because the walk never follows symlinks.
func (s *Sandbox) Denied(absResolved string) bool { return s.denied(absResolved) }

// OpenResolved opens, read-only, a path a walker found below a Check'ed
// directory, skipping Check's per-component symlink resolution: the open
// itself refuses every symlink on the path (openat2 RESOLVE_NO_SYMLINKS,
// the O_NOFOLLOW walk with its post-open verification, or os.Root plus
// final-path verification on Windows), so a path that opens is its own
// resolved path. deny_paths wins (ErrDeniedPath), on the path and after
// the open on the handle identity (hard links to deny targets); a symlink
// anywhere on the path, including one swapped in after the walk listed
// it, fails with ErrOutsideWorkspace. Like OpenFile it returns (nil,
// verdict, nil) when the open needs approval: OutsideRoot outside every
// root, HardLinked for a regular file with more than one link, checked on
// the handle (S16-7). approved accepts both; the returned verdict is
// still the path's.
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
	for _, r := range s.roots {
		if !hasPathPrefix(p, r.path, s.foldRoot) {
			continue
		}
		rel := "."
		if len(p) > len(r.path) {
			rel = p[len(r.path):]
			if os.IsPathSeparator(rel[0]) {
				rel = rel[1:]
			}
		}
		f, err := s.openResolved(r.path, rel, p)
		if err == nil {
			f, err = s.recheckIdentity(f)
		}
		if err != nil {
			return nil, Inside, err
		}
		if !approved {
			if n, lerr := linkCountFile(f); lerr != nil || n > 1 {
				return refuseLinked(f, lerr)
			}
		}
		return f, Inside, nil
	}
	if !approved {
		return nil, OutsideRoot, nil
	}
	// Outside every root: the same no-symlink open, from the volume root,
	// so no ancestor can be swapped for a symlink (CT27-1), plus the full
	// post-open deny re-check of OpenApproved.
	vol := filepath.VolumeName(p) + string(filepath.Separator)
	rel := "."
	if len(p) > len(vol) {
		rel = p[len(vol):]
	}
	f, err := s.openResolved(vol, rel, p)
	if err == nil {
		f, err = s.recheckDeny(f)
	}
	return f, OutsideRoot, err
}

// recheckIdentity is the identity half of recheckDeny (S16-2): the opened
// object must not be a deny target stat'ed at New (hard links included).
// The path half is not repeated: every open path used here refuses
// symlinks on the whole path (openat2 RESOLVE_NO_SYMLINKS) or verifies the
// handle's path (verifyProcFd, verifyInode, GetFinalPathNameByHandle), so
// the handle's path is p, which was matched against deny_paths before the
// open. Saves a readlink per walked file.
func (s *Sandbox) recheckIdentity(f *os.File) (*os.File, error) {
	if len(s.denyIDs) == 0 {
		return f, nil
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, closeFail(f, fmt.Errorf("sandbox: stat: %w", err))
	}
	if slices.ContainsFunc(s.denyIDs, func(d os.FileInfo) bool { return os.SameFile(fi, d) }) {
		return nil, closeFail(f, ErrDeniedPath)
	}
	return f, nil
}
