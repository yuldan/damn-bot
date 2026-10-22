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

	"github.com/damnbot/damnbot/internal/sandbox"
)

// Undoer restores file actions through the sandbox. It never trusts the
// journal alone: the current bytes must equal the recorded before image
// (idempotent no-op) or after image (restore), otherwise the undo refuses
// with ErrConflict unless force is set (SPEC §18.1). Reads and writes are
// opened with sandbox.OpenResolved/OpenFile (no symlink on any component,
// deny_paths re-checked), and unlink/rename use descriptors of the parent
// directory opened the same way, so an ancestor swapped for a symlink after
// Check can redirect neither outside the root (SEC5-12) nor into a
// deny_paths directory inside it (SEC5-15). Roots are pinned and
// identity-checked once per batch (SEC5-16).
type Undoer struct {
	store Store
	sb    *sandbox.Sandbox
	roots map[string]*sandbox.Dir
}

// NewUndoer returns an undo executor over store and the workspace sandbox.
func NewUndoer(store Store, sb *sandbox.Sandbox) (*Undoer, error) {
	if store == nil || sb == nil {
		return nil, errors.New("journal: undoer needs store and sandbox")
	}
	return &Undoer{store: store, sb: sb, roots: map[string]*sandbox.Dir{}}, nil
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
	res, uerr := u.undoAction(ctx, &a, force)
	return u.finish(ctx, caller, res, res.Status, res.Detail, uerr)
}

// undoAction restores the action without recording the undo; Undo/UndoLast
// record one outcome or a batch.
func (u *Undoer) undoAction(ctx context.Context, a *Action, force bool) (Result, error) {
	res := Result{ActionID: a.ID}
	switch {
	case a.Expired:
		res.Status, res.Detail = Expired, "action is outside the retention window"
		return res, ErrExpired
	case a.Reversibility != Reversible:
		res.Status, res.Detail = Failed, "action has no inverse"
		return res, ErrIrreversible
	case a.Open:
		res.Status, res.Detail = Failed, "the action's turn is still open"
		return res, ErrPending
	}
	return u.apply(ctx, a, force)
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
	return u.UndoActions(ctx, caller, acts, force)
}

// UndoActions undoes exactly acts, the set the caller listed and checked,
// and never re-lists: an action appended after the check cannot slip into
// the batch with a different sandbox (SEC5-14).
func (u *Undoer) UndoActions(ctx context.Context, caller string, acts []Action, force bool) ([]Result, error) {
	out := make([]Result, 0, len(acts))
	undos := make([]Undo, 0, len(acts))
	var errs []error
	for i := range acts {
		r, uerr := u.undoAction(ctx, &acts[i], force)
		out = append(out, r)
		errs = append(errs, uerr)
		undos = append(undos, Undo{ActionID: r.ActionID, SessionID: caller, Status: r.Status, Detail: r.Detail})
	}
	errs = append(errs, u.finishBatch(ctx, undos))
	return out, errors.Join(errs...)
}

// finishBatch records all outcomes in one transaction when the store
// supports it (undo N); otherwise it falls back to one commit per action.
func (u *Undoer) finishBatch(ctx context.Context, undos []Undo) error {
	if bf, ok := u.store.(BatchFinisher); ok {
		if err := bf.FinishBatch(ctx, undos); err != nil {
			return fmt.Errorf("journal: record undo batch: %w", err)
		}
		return nil
	}
	var errs []error
	for i := range undos {
		if err := u.store.Finish(ctx, undos[i]); err != nil {
			errs = append(errs, fmt.Errorf("journal: record undo: %w", err))
		}
	}
	return errors.Join(errs...)
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
	cur, resolved, present, hard, err := u.current(ctx, a.Path)
	if err != nil {
		return Result{ActionID: a.ID, Status: statusFor(err)}, err
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
		return u.undoCreate(ctx, resolved, present, hard, force, res)
	}
	data, berr := u.store.Blob(ctx, a.Before.BlobSHA)
	if berr != nil {
		res.Status, res.Files = Failed, nil
		return res, fmt.Errorf("journal: before blob: %w", berr)
	}
	if werr := u.write(ctx, resolved, data, a.Before, hard, force); werr != nil {
		res.Status, res.Files = Failed, nil
		return res, werr
	}
	res.Detail = "restored the before image"
	return res, nil
}

// undoMove reverses Path → MoveTo.
func (u *Undoer) undoMove(ctx context.Context, a *Action, force bool) (Result, error) {
	cur, srcResolved, present, _, err := u.current(ctx, a.Path)
	if err != nil {
		return Result{ActionID: a.ID, Status: statusFor(err)}, err
	}
	dst, dstResolved, dstPresent, _, err := u.current(ctx, a.MoveTo)
	if err != nil {
		return Result{ActionID: a.ID, Status: statusFor(err)}, err
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
	// SEC5-17: validate the rename before the source is removed.
	srcRoot, _, srcOK := u.sb.RootOf(srcResolved)
	dstRoot, _, dstOK := u.sb.RootOf(dstResolved)
	if !srcOK || !dstOK || srcRoot != dstRoot {
		res.Status, res.Detail, res.Files = Failed, "move crosses sandbox roots", nil
		return res, ErrUnsafePath
	}
	if present {
		if rerr := u.remove(ctx, srcResolved); rerr != nil {
			res.Status, res.Files = Failed, nil
			return res, rerr
		}
	}
	if merr := u.rename(ctx, dstResolved, srcResolved); merr != nil {
		res.Status, res.Files = Failed, nil
		return res, merr
	}
	if a.Before != nil && (a.Before.ModeSet || a.Before.Mode != 0) {
		mode := a.Before.Mode & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
		if cerr := chmodFile(ctx, u.sb, srcResolved, mode); cerr != nil {
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

// rootDir pins the sandbox root containing root once per batch (SEC5-16).
func (u *Undoer) rootDir(ctx context.Context, root string) (*sandbox.Dir, error) {
	if d, ok := u.roots[root]; ok {
		return d, nil
	}
	d, err := u.sb.PinRoot(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("journal: root %s: %w", root, err)
	}
	u.roots[root] = d
	return d, nil
}

// parentDir pins the resolved parent directory inside its checked root and
// returns it with the parent path and the final name.
func (u *Undoer) parentDir(ctx context.Context, resolved string) (*sandbox.Dir, string, string, error) {
	root, _, ok := u.sb.RootOf(resolved)
	dir, base := filepath.Split(resolved)
	if !ok || base == "" || base == "." || base == ".." {
		return nil, "", "", fmt.Errorf("journal: %s: %w", resolved, ErrUnsafePath)
	}
	dir = filepath.Clean(dir)
	if _, err := u.rootDir(ctx, root); err != nil {
		return nil, "", "", err
	}
	h, err := u.sb.OpenDir(ctx, dir, false)
	if err != nil {
		return nil, "", "", err //nolint:wrapcheck // sandbox error is already contextual.
	}
	if h == nil {
		return nil, "", "", fmt.Errorf("journal: %s: %w", dir, ErrUnsafePath)
	}
	return h, dir, base, nil
}

// current returns the sha256 of p ("" when absent), its sandbox-resolved
// path, existence and hard-link flag. The open refuses every symlink on the
// path and re-checks deny_paths, so it cannot be redirected after Check
// (SEC5-15). Journaled device/inode is retained for a future two-phase
// after-image settle; it is not enforced because temp+rename changes the
// inode on every legitimate write (ADR-117).
func (u *Undoer) current(ctx context.Context, p string) (sum, resolved string, present, hard bool, err error) {
	resolved, err = u.check(ctx, p)
	if err != nil {
		return "", "", false, false, err
	}
	root, rel, ok := u.sb.MapPath(p)
	if !ok {
		return "", resolved, false, false, fmt.Errorf("%w: %s is outside the sandbox roots", ErrUnsafePath, p)
	}
	if expected := filepath.Join(root, rel); !u.sb.SamePath(expected, resolved) {
		// The journaled path is compared after mapping only the root's own
		// configured spelling (macOS /var, Windows 8.3) to the canonical
		// root. Any further difference is a symlink or a swapped ancestor
		// below the root: a conflict, never a link target (JN2-1, SEC14).
		return "", resolved, false, false, fmt.Errorf("%w: %s is now a symlink", ErrConflict, p)
	}
	f, _, oerr := u.sb.OpenResolved(ctx, resolved, true)
	if errors.Is(oerr, fs.ErrNotExist) {
		return "", resolved, false, false, nil
	}
	if oerr != nil {
		return "", resolved, false, false, fmt.Errorf("%w: open %s: %w", ErrConflict, p, oerr)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	fi, serr := f.Stat()
	if serr != nil || !fi.Mode().IsRegular() {
		return "", resolved, false, false, fmt.Errorf("%w: %s is not a regular file", ErrConflict, p)
	}
	sum, hard, ierr := inspect(f, p)
	if ierr != nil {
		return "", resolved, false, false, ierr
	}
	return sum, resolved, true, hard, nil
}

// inspect hashes an open file and verifies the journaled identity.
func inspect(f *os.File, p string) (sum string, hard bool, err error) {
	fi, err := f.Stat()
	if err != nil {
		return "", false, fmt.Errorf("journal: stat %s: %w", p, err)
	}
	h := sha256.New()
	if _, cerr := io.CopyBuffer(h, f, make([]byte, 64<<10)); cerr != nil {
		return "", false, fmt.Errorf("journal: read %s: %w", p, cerr)
	}
	return hex.EncodeToString(h.Sum(nil)), hardLinks(fi) > 1, nil
}

// check requires a path the sandbox classifies as inside a root and
// returns its resolved absolute path. Hard links are reported separately;
// outside_root and deny_paths are never undoable.
func (u *Undoer) check(ctx context.Context, p string) (string, error) {
	v, resolved, err := u.sb.Check(ctx, p, sandbox.OpWrite)
	if err != nil {
		return "", fmt.Errorf("journal: %s: %w", p, errors.Join(ErrUnsafePath, err))
	}
	if v != sandbox.Inside && v != sandbox.HardLinked {
		return "", fmt.Errorf("journal: %s: %w", p, ErrUnsafePath)
	}
	return resolved, nil
}

// undoCreate removes a path the action created.
func (u *Undoer) undoCreate(ctx context.Context, resolved string, present, hard, force bool, res Result) (Result, error) {
	if !present {
		return res, nil
	}
	if hard && !force {
		res.Status, res.Files = Conflict, nil
		res.Detail = "created path is now a hard link; use --force"
		return res, ErrUnsafePath
	}
	if err := u.remove(ctx, resolved); err != nil {
		res.Status, res.Files = Failed, nil
		return res, err
	}
	res.Detail = "removed the file created by the action"
	return res, nil
}

// write replaces resolved with data and the recorded mode. The temp file is
// written first, chmodded after the bytes (setuid/setgid survive), fsynced
// and renamed through the same pinned parent; hard links are rewritten in
// place only with force (JN2-5/JN2-12).
func (u *Undoer) write(ctx context.Context, resolved string, data []byte, img *Image, hard, force bool) (err error) {
	mode, modeSet := img.Mode, img.ModeSet || img.Mode != 0
	if !modeSet {
		mode = 0o644
	}
	mode &= fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky
	if hard {
		if !force {
			return fmt.Errorf("journal: %s: hard link: %w", resolved, ErrUnsafePath)
		}
		return writeInPlace(ctx, u.sb, resolved, data, mode)
	}
	h, dir, base, err := u.parentDir(ctx, resolved)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, h.Close()) }()
	tmp := base + ".damnbot-undo-" + randHex()
	f, _, err := u.sb.OpenFile(ctx, filepath.Join(dir, tmp), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("journal: temp %s: %w", tmp, err)
	}
	werr := errors.Join(writeAll(f, data), f.Sync(), f.Close())
	if werr != nil {
		return errors.Join(fmt.Errorf("journal: write %s: %w", tmp, werr), removeChild(h, tmp))
	}
	if werr = makeReplaceable(ctx, u.sb, resolved); werr != nil {
		return errors.Join(fmt.Errorf("journal: prepare replace %s: %w", resolved, werr), removeChild(h, tmp))
	}
	if werr = renameChild(h, tmp, h, base); werr != nil {
		return errors.Join(fmt.Errorf("journal: replace %s: %w", resolved, werr), removeChild(h, tmp))
	}
	return errors.Join(chmodFile(ctx, u.sb, resolved, mode), syncDirHandle(h))
}

// chmodFile applies mode to the opened regular file (fchmod), never to a
// symlink swapped in after the check (JN2-1).
func chmodFile(ctx context.Context, sb *sandbox.Sandbox, resolved string, mode fs.FileMode) error {
	f, _, err := sb.OpenResolved(ctx, resolved, true)
	if err != nil {
		return fmt.Errorf("journal: mode %s: %w", resolved, err)
	}
	return errors.Join(f.Chmod(mode), f.Close())
}

func writeInPlace(ctx context.Context, sb *sandbox.Sandbox, resolved string, data []byte, mode fs.FileMode) error {
	f, err := sb.OpenApproved(ctx, resolved, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("journal: open %s: %w", resolved, err)
	}
	werr := f.Truncate(0)
	if werr == nil {
		werr = errors.Join(writeAll(f, data), f.Chmod(mode), f.Sync())
	}
	return errors.Join(werr, f.Close())
}

// remove unlinks resolved through its pinned parent after current verified
// the identity; no textual path is passed to os.Remove (JN2-10).
func (u *Undoer) remove(ctx context.Context, resolved string) (err error) {
	h, _, base, err := u.parentDir(ctx, resolved)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, h.Close()) }()
	if err = removeChild(h, base); err != nil {
		return fmt.Errorf("journal: remove %s: %w", resolved, err)
	}
	return syncDirHandle(h)
}

// rename moves src to dst through their pinned parents; undoMove validated
// that both belong to the same sandbox root (SEC5-17).
func (u *Undoer) rename(ctx context.Context, src, dst string) (err error) {
	srcH, _, srcBase, err := u.parentDir(ctx, src)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, srcH.Close()) }()
	dstH, _, dstBase, err := u.parentDir(ctx, dst)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dstH.Close()) }()
	if err = renameChild(srcH, srcBase, dstH, dstBase); err != nil {
		return fmt.Errorf("journal: rename %s: %w", src, err)
	}
	return errors.Join(syncDirHandle(srcH), syncDirHandle(dstH))
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

func statusFor(err error) Status {
	if errors.Is(err, ErrConflict) {
		return Conflict
	}
	return Failed
}

func match(cur string, img *Image) bool {
	return (cur == "" && img == nil) || (img != nil && cur == img.BlobSHA)
}

// randHex names the undo temp file (collision-resistant; O_EXCL catches the
// astronomically unlikely duplicate).
func randHex() string { return rand.Text()[:12] }
