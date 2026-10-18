package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// DeniedEntry reports, without any syscall, whether the entry name of the
// absolute resolved directory dir matches deny_paths, for a dir that does
// not (walkers, ADR-103: grep/glob skip entries below a Check'ed directory,
// where an entry's lexical path is its resolved path because the walk never
// follows symlinks, and prune denied subdirectories). dir/name can then
// only match a pattern whose last component matches name or is "**", so
// other names cost neither a join nor a split.
func (s *Sandbox) DeniedEntry(dir, name string) bool {
	ascii := isASCII(name)
	for _, pat := range s.deny {
		if last := &pat[len(pat)-1]; last.pat == "**" || last.match(name, ascii) {
			return s.denied(filepath.Join(dir, name))
		}
	}
	return false
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
		if HasPathPrefix(p, r.path, s.foldRoot) {
			dir, v = r.path, Inside
			break
		}
	}
	if v == OutsideRoot && !approved {
		return nil, OutsideRoot, nil
	}
	f, err := s.openBeneath(dir, relTo(dir, p), p, os.O_RDONLY, 0)
	// Inside a root the handle's path needs no re-check (see vet); outside,
	// the full post-open deny re-check of OpenApproved runs too (CT27-1).
	return s.vet(f, err, v, v == OutsideRoot, v == Inside && !approved)
}
