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
// OutsideRoot returns (nil, OutsideRoot, nil): ask approval, then use
// OpenApproved; headless callers fail with ErrOutsideWorkspace.
func (s *Sandbox) OpenFile(ctx context.Context, p string, flag int, perm fs.FileMode) (*os.File, Verdict, error) {
	op := OpRead
	if flag&writeFlags != 0 {
		op = OpWrite
	}
	v, resolved, idx, err := s.check(ctx, p, op)
	if err != nil || v != Inside {
		return nil, v, err
	}
	rootDir := s.roots[idx].path
	rel := "."
	if len(resolved) > len(rootDir) {
		rel = resolved[len(rootDir):]
		if os.IsPathSeparator(rel[0]) {
			rel = rel[1:]
		}
	}
	f, err := s.openBeneath(rootDir, rel, resolved, flag, perm)
	if err == nil {
		f, err = s.recheckDeny(f)
	}
	if err != nil {
		return nil, Inside, err
	}
	return f, Inside, nil
}

// OpenApproved opens p after the user approved an OutsideRoot verdict for
// it. deny_paths still wins (ErrDeniedPath). The resolved target is opened
// beneath its own parent with the same no-symlink guarantees as OpenFile,
// so a symlink swapped in after the approval cannot redirect it; swaps of
// ancestor directories after Check are residual risk (TRUST-MODEL §3).
func (s *Sandbox) OpenApproved(ctx context.Context, p string, flag int, perm fs.FileMode) (*os.File, error) {
	op := OpRead
	if flag&writeFlags != 0 {
		op = OpWrite
	}
	_, resolved, _, err := s.check(ctx, p, op)
	if err != nil {
		return nil, err
	}
	dir, base := filepath.Dir(resolved), filepath.Base(resolved)
	if dir == resolved { // volume root
		base = "."
	}
	f, err := s.openBeneath(dir, base, resolved, flag, perm)
	if err != nil {
		return nil, err
	}
	return s.recheckDeny(f)
}

// recheckDeny is the post-open deny check (S16-2): the handle's real path,
// where the OS reports one (Linux /proc/self/fd, Windows
// GetFinalPathNameByHandle), must not match deny_paths, and the opened
// object must not be one a literal deny pattern named at New (same
// device+inode / volume+file ID). It catches aliases the path checks miss:
// a deny dir or Home symlinked after New, a hard link to a deny file,
// Windows volume aliases. A hit closes f and returns ErrDeniedPath.
func (s *Sandbox) recheckDeny(f *os.File) (*os.File, error) {
	p, ok := handlePath(f)
	denied := ok && s.denied(p)
	if !denied && len(s.denyIDs) > 0 {
		fi, err := f.Stat()
		if err != nil {
			return nil, closeFail(f, fmt.Errorf("sandbox: stat: %w", err))
		}
		denied = slices.ContainsFunc(s.denyIDs, func(d os.FileInfo) bool { return os.SameFile(fi, d) })
	}
	if denied {
		return nil, closeFail(f, ErrDeniedPath)
	}
	return f, nil
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
