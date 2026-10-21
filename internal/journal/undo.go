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
// with ErrConflict unless force is set (SPEC §18.1).
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
	cur, resolved, present, err := u.current(ctx, a.Path)
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
		if present {
			if rerr := u.remove(resolved); rerr != nil {
				res.Status, res.Files = Failed, nil
				return res, rerr
			}
		}
		res.Detail = "removed the file created by the action"
		return res, nil
	}
	data, err := u.store.Blob(ctx, a.Before.BlobSHA)
	if err != nil {
		res.Status, res.Files = Failed, nil
		return res, fmt.Errorf("journal: before blob: %w", err)
	}
	if werr := u.write(ctx, resolved, data, a.Before.Mode, force); werr != nil {
		res.Status, res.Files = Failed, nil
		return res, werr
	}
	res.Detail = "restored the before image"
	if force && !match(cur, a.After) {
		res.Detail = "restored the before image (forced over a changed file)"
	}
	return res, nil
}

// undoMove reverses Path → MoveTo.
func (u *Undoer) undoMove(ctx context.Context, a *Action, force bool) (Result, error) {
	cur, _, present, err := u.current(ctx, a.Path)
	if err != nil {
		return Result{ActionID: a.ID, Status: Failed}, err
	}
	dst, dstResolved, dstPresent, err := u.current(ctx, a.MoveTo)
	if err != nil {
		return Result{ActionID: a.ID, Status: Failed}, err
	}
	res := Result{ActionID: a.ID, Status: Undone, Files: []string{a.Path}}
	switch {
	case match(cur, a.Before) && !dstPresent:
		res.Detail = "already at the before image"
		return res, nil
	case !dstPresent:
		res.Status, res.Files = Failed, nil
		res.Detail = "move destination is gone"
		return res, ErrState
	case !match(dst, a.After) && !force:
		res.Status, res.Files = Conflict, nil
		res.Detail = "move destination changed since the action"
		return res, ErrConflict
	case present && !force:
		res.Status, res.Files = Conflict, nil
		res.Detail = "move destination and source both exist"
		return res, ErrConflict
	}
	_, srcResolved, err := u.check(ctx, a.Path)
	if err != nil {
		res.Status, res.Files = Failed, nil
		return res, err
	}
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
	res.Detail = "moved the destination back to the source"
	return res, nil
}

// current returns the sha256 of p ("" when absent), its sandbox-resolved
// path and existence. Outside, denied and non-regular paths are refused.
func (u *Undoer) current(ctx context.Context, p string) (sum, resolved string, present bool, err error) {
	_, resolved, err = u.check(ctx, p)
	if err != nil {
		return "", "", false, err
	}
	f, verdict, err := u.sb.OpenFile(ctx, p, os.O_RDONLY, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return "", resolved, false, nil
	}
	if err != nil {
		return "", resolved, false, fmt.Errorf("journal: open %s: %w", p, err)
	}
	if f == nil && verdict == sandbox.HardLinked {
		if f, err = u.sb.OpenApproved(ctx, p, os.O_RDONLY, 0); err != nil {
			return "", resolved, false, fmt.Errorf("journal: open %s: %w", p, err)
		}
	}
	if f == nil {
		return "", resolved, false, fmt.Errorf("journal: open %s: %w", p, ErrUnsafePath)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("journal: close %s: %w", p, cerr)
		}
	}()
	b, err := io.ReadAll(f)
	if err != nil {
		return "", resolved, false, fmt.Errorf("journal: read %s: %w", p, err)
	}
	return hash(b), resolved, true, nil
}

// check requires a path the sandbox classifies as inside a root. Hard links
// are reported separately by current/write; outside_root and deny_paths are
// never undoable.
func (u *Undoer) check(ctx context.Context, p string) (verdict sandbox.Verdict, resolved string, err error) {
	v, resolved, err := u.sb.Check(ctx, p, sandbox.OpWrite)
	if err != nil {
		return v, resolved, fmt.Errorf("journal: %s: %w", p, errors.Join(ErrUnsafePath, err))
	}
	if v != sandbox.Inside && v != sandbox.HardLinked {
		return v, resolved, fmt.Errorf("journal: %s: %w", p, ErrUnsafePath)
	}
	return v, resolved, nil
}

// write replaces resolved with data and mode. A hard link is rewritten in
// place only when force is set (the link must keep its inode); otherwise it
// is refused like the codetools approval verdict.
func (u *Undoer) write(ctx context.Context, resolved string, data []byte, mode fs.FileMode, force bool) error {
	if mode == 0 {
		mode = 0o644
	}
	mode &= fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky
	v, _, err := u.check(ctx, resolved)
	if err != nil {
		return err
	}
	if v == sandbox.HardLinked {
		if !force {
			return fmt.Errorf("journal: %s: hard link: %w", resolved, ErrUnsafePath)
		}
		f, oerr := u.sb.OpenApproved(ctx, resolved, os.O_WRONLY|os.O_TRUNC, 0)
		if oerr != nil {
			return fmt.Errorf("journal: open %s: %w", resolved, oerr)
		}
		werr := writeAll(f, data)
		if serr := f.Sync(); werr == nil {
			werr = serr
		}
		return errors.Join(werr, f.Close())
	}
	tmp := filepath.Join(filepath.Dir(resolved), ".damnbot-undo-"+randHex())
	f, _, err := u.sb.OpenFile(ctx, tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("journal: temp %s: %w", tmp, err)
	}
	if cerr := f.Chmod(mode); cerr != nil {
		xerr := f.Close()
		rerr := os.Remove(tmp)
		return fmt.Errorf("journal: mode %s: %w", tmp, errors.Join(cerr, xerr, rerr))
	}
	werr := writeAll(f, data)
	if serr := f.Sync(); werr == nil {
		werr = serr
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		rerr := os.Remove(tmp)
		return fmt.Errorf("journal: write %s: %w", tmp, errors.Join(werr, rerr))
	}
	if rerr := replaceFile(tmp, resolved); rerr != nil {
		xerr := os.Remove(tmp)
		return fmt.Errorf("journal: replace %s: %w", resolved, errors.Join(rerr, xerr))
	}
	return syncDir(filepath.Dir(resolved))
}

func (u *Undoer) remove(resolved string) error {
	if err := os.Remove(resolved); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("journal: remove %s: %w", resolved, err)
	}
	return syncDir(filepath.Dir(resolved))
}

func (u *Undoer) rename(ctx context.Context, src, dst string) error {
	if _, _, err := u.check(ctx, src); err != nil {
		return err
	}
	if _, _, err := u.check(ctx, dst); err != nil {
		return err
	}
	if err := replaceFile(src, dst); err != nil {
		return fmt.Errorf("journal: rename %s: %w", src, err)
	}
	return errors.Join(syncDir(filepath.Dir(src)), syncDir(filepath.Dir(dst)))
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

func hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func match(cur string, img *Image) bool {
	return (cur == "" && img == nil) || (img != nil && cur == img.BlobSHA)
}

// randHex names the undo temp file (collision-resistant; O_EXCL catches the
// astronomically unlikely duplicate).
func randHex() string { return rand.Text()[:12] }
