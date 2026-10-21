package journal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/damnbot/damnbot/internal/sandbox"
)

// Undoer restores file actions through the sandbox. It never trusts the
// journal alone: the current bytes must equal the recorded before image
// (idempotent no-op) or after image (restore), otherwise the undo refuses
// with ErrConflict unless force is set (SPEC §18.1). Every file operation
// goes through an os.Root of the parent directory with O_NOFOLLOW on the
// final component, and the journaled device/inode is compared before
// mutating (JN2-1/JN2-10).
type Undoer struct {
	store Store
	sb    *sandbox.Sandbox
}

// NewUndoer returns an undo executor over store and the workspace sandbox.
func NewUndoer(store Store, sb *sandbox.Sandbox) (*Undoer, error) {
	if store == nil || sb == nil {
		return nil, errors.New("journal: undoer needs store and sandbox")
	}
	return &Undoer{store: store, sb: sb}, nil
}

// Undo restores one action and records the outcome in caller's session. The
// returned Result is populated even when err is non-nil (failed mid-way,
// conflict, lease fenced) so the caller can report every file already
// restored.
func (u *Undoer) Undo(ctx context.Context, caller string, id int64, force bool) (Result, error) {
	a, err := u.store.Get(ctx, id)
	if err != nil {
		return Result{ActionID: id, Status: Failed}, fmt.Errorf("journal: get action %d: %w", id, err)
	}
	res := Result{ActionID: id}
	switch {
	case a.Expired:
		return u.finish(ctx, caller, res, Expired, "action is outside the retention window", ErrExpired)
	case a.Reversibility != Reversible:
		return u.finish(ctx, caller, res, Failed, "action has no inverse", ErrIrreversible)
	case a.Open:
		return u.finish(ctx, caller, res, Failed, "the action's turn is still open", ErrPending)
	}
	res, err = u.apply(ctx, &a, force)
	return u.finish(ctx, caller, res, res.Status, res.Detail, err)
}

// UndoLast undoes the newest n actions of session in reverse order. Every
// action is attempted; errors are joined.
func (u *Undoer) UndoLast(ctx context.Context, caller string, n int, force bool) ([]Result, error) {
	if n <= 0 {
		return nil, fmt.Errorf("journal: undo count %d: %w", n, ErrIrreversible)
	}
	acts, err := u.store.List(ctx, caller, n)
	if err != nil {
		return nil, fmt.Errorf("journal: list actions: %w", err)
	}
	out := make([]Result, 0, len(acts))
	var errs []error
	for i := range acts {
		r, uerr := u.Undo(ctx, caller, acts[i].ID, force)
		out = append(out, r)
		errs = append(errs, uerr)
	}
	return out, errors.Join(errs...)
}

func (u *Undoer) finish(ctx context.Context, caller string, r Result, status Status, detail string, err error) (Result, error) {
	r.Status, r.Detail = status, detail
	ferr := u.store.Finish(ctx, Undo{ActionID: r.ActionID, SessionID: caller, Status: status, Detail: detail})
	if ferr != nil {
		ferr = fmt.Errorf("journal: record undo: %w", ferr)
	}
	return r, errors.Join(err, ferr)
}

func (u *Undoer) apply(ctx context.Context, a *Action, force bool) (Result, error) {
	switch a.Kind {
	case FileMove:
		return u.undoMove(ctx, a, force)
	case FileWrite, FileEdit, FileDelete:
		return u.undoPath(ctx, a, force)
	}
	return Result{ActionID: a.ID, Status: Failed}, fmt.Errorf("journal: kind %q: %w", a.Kind, ErrIrreversible)
}

// undoPath covers write/edit/create/delete: all are one path whose before
// image is either present or nil.
func (u *Undoer) undoPath(ctx context.Context, a *Action, force bool) (Result, error) {
	cur, resolved, present, hard, err := u.current(ctx, a.Path, a.Before)
	if err != nil {
		return Result{ActionID: a.ID, Status: Failed}, err
	}
	res := Result{ActionID: a.ID, Status: Undone, Files: []string{a.Path}}
	switch {
	case match(cur, a.Before):
		res.Detail = "already at the before image"
		return res, nil
	case !match(cur, a.After) && !force:
		res.Status, res.Files = Conflict, nil
		res.Detail = "file changed since the action"
		return res, ErrConflict
	}
	if a.Before == nil {
		return u.undoCreate(resolved, present, hard, force, res)
	}
	data, berr := u.store.Blob(ctx, a.Before.BlobSHA)
	if berr != nil {
		res.Status, res.Files = Failed, nil
		return res, fmt.Errorf("journal: before blob: %w", berr)
	}
	if werr := u.write(resolved, data, a.Before, hard, force); werr != nil {
		res.Status, res.Files = Failed, nil
		return res, werr
	}
	res.Detail = "restored the before image"
	return res, nil
}

// undoMove reverses Path → MoveTo.
func (u *Undoer) undoMove(ctx context.Context, a *Action, force bool) (Result, error) {
	cur, _, present, _, err := u.current(ctx, a.Path, a.Before)
	if err != nil {
		return Result{ActionID: a.ID, Status: Failed}, err
	}
	dst, dstResolved, dstPresent, _, err := u.current(ctx, a.MoveTo, nil)
	if err != nil {
		return Result{ActionID: a.ID, Status: Failed}, err
	}
	res := Result{ActionID: a.ID, Status: Undone, Files: []string{a.Path}}
	st, detail, oerr := moveOutcome(a, cur, dst, present, dstPresent, force)
	if oerr != nil {
		res.Status, res.Detail, res.Files = st, detail, nil
		return res, oerr
	}
	if detail != "" {
		res.Detail = detail
		return res, nil
	}
	srcResolved := filepath.Clean(a.Path)
	if present {
		if rerr := u.remove(srcResolved); rerr != nil {
			res.Status, res.Files = Failed, nil
			return res, rerr
		}
	}
	if merr := u.rename(ctx, dstResolved, srcResolved); merr != nil {
		res.Status, res.Files = Failed, nil
		return res, merr
	}
	if a.Before != nil && (a.Before.ModeSet || a.Before.Mode != 0) {
		if cerr := u.chmod(srcResolved, a.Before.Mode); cerr != nil {
			res.Status, res.Files = Failed, nil
			return res, cerr
		}
	}
	res.Detail = "moved the destination back to the source"
	return res, nil
}

// moveOutcome classifies the state of a move before touching the files.
func moveOutcome(a *Action, cur, dst string, present, dstPresent, force bool) (Status, string, error) {
	switch {
	case match(cur, a.Before) && !dstPresent:
		return Undone, "already at the before image", nil
	case !dstPresent:
		return Failed, "move destination is gone", ErrState
	case !match(dst, a.After) && !force:
		return Conflict, "move destination changed since the action", ErrConflict
	case present && !force:
		return Conflict, "move destination and source both exist", ErrConflict
	}
	return Undone, "", nil
}

// current returns the sha256 of p ("" when absent), its sandbox-resolved
// path, existence and hard-link flag. The final component is opened with
// O_NOFOLLOW through an os.Root, and the device/inode is compared with want
// when recorded, so a swapped symlink or inode is a conflict (JN2-1).
func (u *Undoer) current(ctx context.Context, p string, want *Image) (sum, resolved string, present, hard bool, err error) {
	clean := filepath.Clean(p)
	if cerr := u.check(ctx, p); cerr != nil {
		return "", "", false, false, cerr
	}
	root, base, rerr := parentRoot(clean)
	if errors.Is(rerr, fs.ErrNotExist) {
		return "", p, false, false, nil
	}
	if rerr != nil {
		return "", p, false, false, rerr
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	f, oerr := root.OpenFile(base, os.O_RDONLY|oNoFollow, 0)
	if errors.Is(oerr, fs.ErrNotExist) {
		return "", p, false, false, nil
	}
	if oerr != nil {
		return "", p, false, false, fmt.Errorf("%w: open %s: %w", ErrConflict, p, oerr)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	sum, hard, ierr := inspect(f, want, p)
	if ierr != nil {
		return "", p, false, false, ierr
	}
	return sum, p, true, hard, nil
}

// inspect hashes an open file and verifies the journaled identity.
func inspect(f *os.File, want *Image, p string) (sum string, hard bool, err error) {
	fi, err := f.Stat()
	if err != nil {
		return "", false, fmt.Errorf("journal: stat %s: %w", p, err)
	}
	dev, ino := statIdentity(fi)
	if want != nil && (want.Device != 0 || want.Inode != 0) && (dev != want.Device || ino != want.Inode) {
		return "", false, fmt.Errorf("%w: %s was replaced", ErrConflict, p)
	}
	h := sha256.New()
	if _, cerr := io.CopyBuffer(h, f, make([]byte, 64<<10)); cerr != nil {
		return "", false, fmt.Errorf("journal: read %s: %w", p, cerr)
	}
	return hex.EncodeToString(h.Sum(nil)), hardLinks(fi) > 1, nil
}

// check requires a path the sandbox classifies as inside a root. Hard links
// are reported separately; outside_root and deny_paths are never undoable.
func (u *Undoer) check(ctx context.Context, p string) error {
	v, _, err := u.sb.Check(ctx, p, sandbox.OpWrite)
	if err != nil {
		return fmt.Errorf("journal: %s: %w", p, errors.Join(ErrUnsafePath, err))
	}
	if v != sandbox.Inside && v != sandbox.HardLinked {
		return fmt.Errorf("journal: %s: %w", p, ErrUnsafePath)
	}
	return nil
}

// undoCreate removes a path the action created.
func (u *Undoer) undoCreate(resolved string, present, hard, force bool, res Result) (Result, error) {
	if !present {
		return res, nil
	}
	if hard && !force {
		res.Status, res.Files = Conflict, nil
		res.Detail = "created path is now a hard link; use --force"
		return res, ErrUnsafePath
	}
	if err := u.remove(resolved); err != nil {
		res.Status, res.Files = Failed, nil
		return res, err
	}
	res.Detail = "removed the file created by the action"
	return res, nil
}

// write replaces resolved with data and the recorded mode. The temp file is
// written first, chmodded after the bytes (setuid/setgid survive), fsynced
// and renamed through the same directory root; hard links are rewritten in
// place only with force (JN2-5/JN2-12).
func (u *Undoer) write(resolved string, data []byte, img *Image, hard, force bool) (err error) {
	mode, modeSet := img.Mode, img.ModeSet || img.Mode != 0
	if !modeSet {
		mode = 0o644
	}
	mode &= fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky
	root, base, err := parentRoot(resolved)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if hard {
		if !force {
			return fmt.Errorf("journal: %s: hard link: %w", resolved, ErrUnsafePath)
		}
		return writeInPlace(root, base, data, mode)
	}
	tmp := base + ".damnbot-undo-" + randHex()
	f, err := root.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("journal: temp %s: %w", tmp, err)
	}
	werr := writeAll(f, data)
	if werr == nil {
		werr = f.Chmod(mode)
	}
	if werr == nil {
		werr = f.Sync()
	}
	werr = errors.Join(werr, f.Close())
	if werr != nil {
		return errors.Join(fmt.Errorf("journal: write %s: %w", tmp, werr), root.Remove(tmp))
	}
	if werr = root.Rename(tmp, base); werr != nil {
		return errors.Join(fmt.Errorf("journal: replace %s: %w", resolved, werr), root.Remove(tmp))
	}
	return syncDir(filepath.Dir(resolved))
}

func writeInPlace(root *os.Root, base string, data []byte, mode fs.FileMode) error {
	f, err := root.OpenFile(base, os.O_WRONLY|oNoFollow, 0)
	if err != nil {
		return fmt.Errorf("journal: open %s: %w", base, err)
	}
	werr := f.Truncate(0)
	if werr == nil {
		werr = writeAll(f, data)
	}
	if werr == nil {
		werr = f.Chmod(mode)
	}
	if werr == nil {
		werr = f.Sync()
	}
	return errors.Join(werr, f.Close())
}

// remove unlinks the final component through its directory root after
// current verified the identity; no textual path is passed to os.Remove
// (JN2-10).
func (u *Undoer) remove(resolved string) (err error) {
	root, base, err := parentRoot(resolved)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if err = root.Remove(base); err != nil {
		return fmt.Errorf("journal: remove %s: %w", resolved, err)
	}
	return syncDir(filepath.Dir(resolved))
}

// rename moves src to dst through a root at their common ancestor, so both
// names stay dirfd-relative (JN2-10).
func (u *Undoer) rename(ctx context.Context, src, dst string) (err error) {
	if serr := u.check(ctx, src); serr != nil {
		return serr
	}
	if derr := u.check(ctx, dst); derr != nil {
		return derr
	}
	common := commonDir(filepath.Dir(src), filepath.Dir(dst))
	root, err := os.OpenRoot(common)
	if err != nil {
		return fmt.Errorf("journal: open dir %s: %w", common, err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	relSrc, err := filepath.Rel(common, src) // common is an ancestor of both
	if err != nil {
		return fmt.Errorf("journal: relative %s: %w", src, err)
	}
	relDst, err := filepath.Rel(common, dst)
	if err != nil {
		return fmt.Errorf("journal: relative %s: %w", dst, err)
	}
	if err = root.Rename(relSrc, relDst); err != nil {
		return fmt.Errorf("journal: rename %s: %w", src, err)
	}
	return errors.Join(syncDir(filepath.Dir(src)), syncDir(filepath.Dir(dst)))
}

func (u *Undoer) chmod(resolved string, mode fs.FileMode) (err error) {
	root, base, err := parentRoot(resolved)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	mode &= fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky
	if err = root.Chmod(base, mode); err != nil {
		return fmt.Errorf("journal: chmod %s: %w", resolved, err)
	}
	return nil
}

// parentRoot opens the directory of resolved; the final component is passed
// to Root methods so no symlink in it is followed.
func parentRoot(resolved string) (*os.Root, string, error) {
	dir, base := filepath.Split(resolved)
	if base == "" || base == "." || base == ".." {
		return nil, "", fmt.Errorf("journal: %s: %w", resolved, ErrUnsafePath)
	}
	root, err := os.OpenRoot(filepath.Clean(dir))
	if err != nil {
		return nil, "", fmt.Errorf("journal: open dir %s: %w", filepath.Clean(dir), err)
	}
	return root, base, nil
}

// commonDir is the deepest directory containing both paths.
func commonDir(a, b string) string {
	for {
		rel, err := filepath.Rel(a, b)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return a
		}
		parent := filepath.Dir(a)
		if parent == a {
			return a
		}
		a = parent
	}
}

func writeAll(f *os.File, data []byte) error {
	for len(data) > 0 {
		n, err := f.Write(data)
		if err != nil {
			return fmt.Errorf("journal: write: %w", err)
		}
		data = data[n:]
	}
	return nil
}

func match(cur string, img *Image) bool {
	return (cur == "" && img == nil) || (img != nil && cur == img.BlobSHA)
}

// randHex names the undo temp file (collision-resistant; O_EXCL catches the
// astronomically unlikely duplicate).
func randHex() string { return rand.Text()[:12] }
