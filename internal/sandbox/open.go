package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// writeFlags are the os.OpenFile flags that make an open a write.
const writeFlags = os.O_WRONLY | os.O_RDWR | os.O_CREATE | os.O_TRUNC | os.O_APPEND

// OpenFile checks p like Check (op derived from flag) and, when Inside,
// opens the resolved path beneath its root without following any symlink
// that appeared after the check (anti-TOCTOU, CODE-TOOLS §8):
//   - Linux ≥ 5.6: openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|
//     RESOLVE_NO_MAGICLINKS) relative to the root;
//   - other Unix and Linux < 5.6: per-component openat(O_NOFOLLOW) walk
//     from the root, plus /proc/self/fd verification on Linux;
//   - Windows: os.Root, then GetFinalPathNameByHandle must equal the path.
//
// An escape or a lost race closes the file and returns ErrOutsideWorkspace.
// OutsideRoot and HardLinked return (nil, verdict, nil): ask approval, then
// use OpenApproved; headless callers fail with ErrOutsideWorkspace. A
// regular file that gained a second link after Check is closed and
// reported HardLinked (S16-7); O_TRUNC is applied only after the handle
// passed every check, so a refused file is never truncated. A FIFO, socket
// or device is refused with ErrNotRegular, without blocking (CT27-4): the
// last component is opened O_NONBLOCK on Unix and its type checked on the
// handle (GetFileType on Windows).
func (s *Sandbox) OpenFile(ctx context.Context, p string, flag int, perm fs.FileMode) (*os.File, Verdict, error) {
	v, resolved, idx, err := s.check(ctx, p, opFor(flag))
	if err != nil || v != Inside {
		return nil, v, err
	}
	return s.openVerified(s.roots[idx].path, resolved, flag, perm, true)
}

// OpenApproved opens p after the user approved an OutsideRoot or
// HardLinked verdict for it. deny_paths still wins (ErrDeniedPath). The resolved target is opened
// beneath its own parent with the same no-symlink guarantees as OpenFile,
// so a symlink swapped in after the approval cannot redirect it; swaps of
// ancestor directories after Check are residual risk (TRUST-MODEL §3).
func (s *Sandbox) OpenApproved(ctx context.Context, p string, flag int, perm fs.FileMode) (*os.File, error) {
	_, resolved, _, err := s.check(ctx, p, opFor(flag))
	if err != nil {
		return nil, err
	}
	f, _, err := s.openVerified(filepath.Dir(resolved), resolved, flag, perm, false)
	return f, err
}

func opFor(flag int) Op {
	if flag&writeFlags != 0 {
		return OpWrite
	}
	return OpRead
}

// openVerified opens want beneath dir without O_TRUNC, vets the handle
// (vet: deny_paths by path and identity; HardLinked when single), and
// only then truncates.
func (s *Sandbox) openVerified(dir, want string, flag int, perm fs.FileMode, single bool) (*os.File, Verdict, error) {
	f, err := s.openBeneath(dir, relTo(dir, want), want, flag&^os.O_TRUNC, perm)
	f, v, err := s.vet(f, err, Inside, true, single)
	if f != nil && flag&os.O_TRUNC != 0 {
		if terr := f.Truncate(0); terr != nil {
			return nil, Inside, closeFail(f, fmt.Errorf("sandbox: truncate: %w", terr))
		}
	}
	return f, v, err
}

// relTo is p relative to dir, a prefix of p ending on a component
// boundary; "." for p itself.
func relTo(dir, p string) string {
	rel := p[len(dir):]
	if rel != "" && os.IsPathSeparator(rel[0]) {
		rel = rel[1:]
	}
	if rel == "" {
		return "."
	}
	return rel
}

// vet runs the post-open checks on f, the result of an open that failed
// with err or not, and returns verdict v unless refused:
//   - deny_paths (S16-2): the handle's real path, when byPath and the OS
//     reports one (Linux /proc/self/fd, Windows GetFinalPathNameByHandle),
//     must not match, and the opened object must not be one a literal deny
//     pattern named at New (same device+inode / volume+file ID). It catches
//     aliases the path checks miss: a deny dir or Home symlinked after New,
//     a hard link to a deny file, Windows volume aliases. A hit closes f
//     and returns ErrDeniedPath. byPath is needless where the open refused
//     symlinks on the whole path (OpenResolved inside a root): the
//     handle's path is the one already matched, saving a readlink;
//   - single: a regular file with more than one link is closed and
//     reported (nil, HardLinked, nil) (S16-7); an unreadable link count
//     fails closed.
func (s *Sandbox) vet(f *os.File, err error, v Verdict, byPath, single bool) (*os.File, Verdict, error) {
	if err != nil {
		return nil, v, err
	}
	denied := false
	if byPath {
		p, ok := handlePath(f)
		denied = ok && s.denied(p)
	}
	if !denied && len(s.denyIDs) > 0 {
		fi, serr := f.Stat()
		if serr != nil {
			return nil, v, closeFail(f, fmt.Errorf("sandbox: stat: %w", serr))
		}
		denied = slices.ContainsFunc(s.denyIDs, func(d os.FileInfo) bool { return os.SameFile(fi, d) })
	}
	if denied {
		return nil, v, closeFail(f, ErrDeniedPath)
	}
	if !single {
		return f, v, nil
	}
	switch n, lerr := linkCountFile(f); {
	case lerr != nil:
		return nil, v, closeFail(f, lerr)
	case n > 1:
		return nil, HardLinked, closeFail(f, nil) // nil unless Close fails
	}
	return f, v, nil
}

// closeFail closes f, opened but refused, and returns err (plus any close
// error).
func closeFail(f *os.File, err error) error {
	if cerr := f.Close(); cerr != nil {
		return errors.Join(err, fmt.Errorf("sandbox: close: %w", cerr))
	}
	return err
}

// escaped wraps an escape/race failure as ErrOutsideWorkspace, keeping the
// OS cause for diagnostics.
func escaped(want string, cause error) error {
	return fmt.Errorf("%w: %s: %w", ErrOutsideWorkspace, want, cause)
}

// closeDir closes a directory handle opened only for path lookups: nothing
// was written through it, so a close error cannot lose data.
func closeDir(c io.Closer) {
	c.Close() //nolint:errcheck,gosec // lookup-only directory handle: close cannot lose data.
}
