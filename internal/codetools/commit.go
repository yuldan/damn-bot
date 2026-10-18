package codetools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/damnbot/damnbot/internal/sandbox"
)

// tempMarker names the atomic-write temp files: ".<name>.damnbot-<rand>"
// (CODE-TOOLS §5.4), removed when orphaned by CleanOrphanTemps.
const tempMarker = ".damnbot-"

// commitInfo is what the write path reports in Meta (P14).
type commitInfo struct {
	ownerOK, modeOK, xattrOK bool
	inplace                  bool
	sum                      [32]byte
	key                      fileKey
}

// commit replaces t's file with data (CODE-TOOLS §5.4): temp in the
// resolved directory, mode set before the first byte, fsync(file), owner
// and xattrs copied, re-stat against before (stale_read if changed),
// rename + fsync(dir), then a read-back sha256 check (TOOL-03). A
// hard-linked target is rewritten in place instead (rename would break the
// link). mode 0 creates a new file with the umask-derived mode. On any
// failure the target keeps its old bytes.
func (e *Engine) commit(ctx context.Context, t *target, before fileKey, mode fs.FileMode, data []byte) (commitInfo, error) {
	return e.commitStream(ctx, t, before, mode, func(w io.Writer) error {
		_, err := w.Write(data)
		if err != nil {
			return fmt.Errorf("codetools: write: %w", err)
		}
		return nil
	})
}

// commitStream is commit for content produced by fill (streaming edits);
// an error from fill aborts the write and is returned as is when it is a
// *ToolError.
func (e *Engine) commitStream(ctx context.Context, t *target, before fileKey, mode fs.FileMode, fill func(io.Writer) error) (ci commitInfo, err error) {
	ci = commitInfo{ownerOK: true, modeOK: true, xattrOK: true}
	if t.f != nil && linkCount(t.f) > 1 {
		ci.inplace = true
		err = e.writeInPlace(ctx, t, before, fill, &ci)
		return ci, err
	}
	dir, name := filepath.Dir(t.resolved), filepath.Base(t.resolved)
	tmpName := filepath.Join(dir, "."+name+tempMarker+randHex())
	perm := fs.FileMode(0o600) // existing file: final mode set below
	if mode == 0 {
		perm = 0o666 // new file: the process umask applies
	}
	tmp, err := e.openWrite(ctx, tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm, t.outside)
	if err != nil {
		return ci, err
	}
	done := false
	defer func() {
		if !done {
			err = errors.Join(err, tmp.Close(), os.Remove(tmpName))
		}
	}()
	// Owner and xattrs first (xattr writes need a writable inode), then the
	// final mode before the first byte (§5.4 langkah 2).
	if t.f != nil {
		ci.ownerOK = copyOwner(t.f, tmp)
		ci.xattrOK = copyXattrs(t.f, tmp)
	}
	if mode != 0 {
		ci.modeOK = tmp.Chmod(mode) == nil
	}
	if werr := hashFill(tmp, fill, &ci); werr != nil {
		return ci, fillErr("write", t.rel, werr)
	}
	if werr := tmp.Sync(); werr != nil {
		return ci, ioErr("fsync", t.rel, werr)
	}
	if werr := e.unchanged(t, before); werr != nil {
		return ci, werr
	}
	if werr := tmp.Close(); werr != nil {
		return ci, ioErr("close", t.rel, werr)
	}
	done = true
	if werr := renameReplace(tmpName, t.resolved); werr != nil {
		return ci, errors.Join(ioErr("rename", t.rel, werr), os.Remove(tmpName))
	}
	if werr := e.syncDir(ctx, dir, t.outside); werr != nil {
		return ci, ioErr("fsync dir", t.rel, werr)
	}
	err = e.readBack(ctx, t, &ci)
	return ci, err
}

// syncDir fsyncs the directory of a renamed file, opened through the sandbox.
func (e *Engine) syncDir(ctx context.Context, dir string, outside bool) error {
	if !dirSyncable {
		return nil
	}
	d, err := e.sb.OpenResolved(ctx, dir, outside)
	if err != nil {
		return fmt.Errorf("codetools: open dir: %w", err)
	}
	if err := errors.Join(d.Sync(), d.Close()); err != nil {
		return fmt.Errorf("codetools: fsync dir: %w", err)
	}
	return nil
}

// unchanged re-stats the target (§5.4 langkah 4): size, mtime and inode
// must equal the state the edit was computed from.
func (e *Engine) unchanged(t *target, before fileKey) error {
	if before == (fileKey{}) {
		if _, err := os.Lstat(t.resolved); !errors.Is(err, fs.ErrNotExist) {
			return toolErr(CodeStaleRead, "read_file first", "%s was created concurrently", t.rel)
		}
		return nil
	}
	fi, err := os.Lstat(t.resolved)
	if err != nil || keyOf(t.resolved, fi) != before {
		return toolErr(CodeStaleRead, "read the file again", "%s changed while writing", t.rel)
	}
	return nil
}

func (e *Engine) writeInPlace(ctx context.Context, t *target, before fileKey, fill func(io.Writer) error, ci *commitInfo) error {
	if err := e.unchanged(t, before); err != nil {
		return err
	}
	f, err := e.openWrite(ctx, t.resolved, os.O_WRONLY|os.O_TRUNC, 0, t.outside)
	if err != nil {
		return err
	}
	werr := hashFill(f, fill, ci)
	if werr == nil {
		werr = f.Sync()
	}
	if err := errors.Join(werr, f.Close()); err != nil {
		return fillErr("write in place", t.rel, err)
	}
	return e.readBack(ctx, t, ci)
}

// hashFill runs fill into w through a buffer while hashing the bytes.
func hashFill(w io.Writer, fill func(io.Writer) error, ci *commitInfo) error {
	h := sha256.New()
	bw := bufio.NewWriterSize(io.MultiWriter(w, h), 1<<20)
	if err := fill(bw); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("codetools: flush: %w", err)
	}
	copy(ci.sum[:], h.Sum(nil))
	return nil
}

// fillErr keeps a *ToolError from fill (validation during streaming) and
// classifies I/O errors.
func fillErr(op, rel string, err error) error {
	if te, ok := errors.AsType[*ToolError](err); ok {
		return te
	}
	return ioErr(op, rel, err)
}

// readBack re-reads the committed file and compares sha256 (TOOL-03).
func (e *Engine) readBack(ctx context.Context, t *target, ci *commitInfo) (err error) {
	f, err := e.sb.OpenResolved(ctx, t.resolved, t.outside)
	if err != nil {
		return mapOpenErr(t.rel, err)
	}
	defer closeInto(&err, f)
	h := sha256.New()
	if _, cerr := io.Copy(h, f); cerr != nil {
		return ioErr("read back", t.rel, cerr)
	}
	fi, err := f.Stat()
	if err != nil {
		return ioErr("read back", t.rel, err)
	}
	if !bytes.Equal(h.Sum(nil), ci.sum[:]) {
		return toolErr(CodeWriteVerifyFailed, "the file may have been changed concurrently; read it again", "%s: read-back sha256 differs from the written bytes", t.rel)
	}
	ci.key = keyOf(t.resolved, fi)
	return nil
}

// openWrite opens a path for writing through the sandbox; approved paths
// outside the roots use OpenApproved (SPEC §4.3 langkah 7).
func (e *Engine) openWrite(ctx context.Context, p string, flag int, perm fs.FileMode, outside bool) (*os.File, error) {
	f, v, err := e.sb.OpenFile(ctx, p, flag, perm)
	if err == nil && v == sandbox.OutsideRoot {
		if !outside {
			return nil, toolErr(CodeOutsideWorkspace, "paths outside the workspace need approval", "%s is outside the workspace", p)
		}
		f, err = e.sb.OpenApproved(ctx, p, flag, perm)
	}
	if err != nil {
		return nil, mapOpenErr(e.rel(p), err)
	}
	return f, nil
}

// mkdirs creates the missing parents of p inside the sandbox, checking
// each new directory before creating it.
func (e *Engine) mkdirs(ctx context.Context, dir string, approved bool) error {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil || filepath.Dir(d) == d {
			break
		}
		missing = append(missing, d)
	}
	for _, d := range slices.Backward(missing) {
		v, _, err := e.sb.Check(ctx, d, sandbox.OpWrite)
		switch {
		case err != nil:
			return mapOpenErr(e.rel(d), err)
		case v == sandbox.OutsideRoot && !approved:
			return toolErr(CodeOutsideWorkspace, "paths outside the workspace need approval", "%s is outside the workspace", d)
		}
		if merr := os.Mkdir(d, 0o750); merr != nil && !errors.Is(merr, fs.ErrExist) {
			return ioErr("mkdir", e.rel(d), merr)
		}
	}
	return nil
}

// ioErr classifies a write-path failure: disk full is disk_full.
func ioErr(op, rel string, err error) *ToolError {
	if isDiskFull(err) {
		return &ToolError{Code: CodeDiskFull, Message: op + " " + rel + ": disk full", Err: err}
	}
	return &ToolError{Code: CodeInternal, Message: op + " " + rel, Err: err}
}

func randHex() string { return strings.ToLower(rand.Text()[:12]) }

// CleanOrphanTemps removes ".<name>.damnbot-<rand>" files left by a crash
// in the session roots (CODE-TOOLS §5.4); call it at session start.
func (e *Engine) CleanOrphanTemps(ctx context.Context) (int, error) {
	removed := 0
	for _, root := range e.roots {
		r, err := e.searchRoot(ctx, root, Opts{})
		if err != nil {
			return removed, err
		}
		var found []string
		spec := &walkSpec{root: r.abs, hidden: true, keepFile: func(rel string) bool {
			base := filepath.Base(rel)
			return base[0] == '.' && strings.Contains(base, tempMarker)
		}}
		if _, err := e.walk(ctx, spec, r.st, r.top, func(j *fileJob, _ scanOut) bool {
			found = append(found, j.abs)
			return true
		}); err != nil {
			return removed, fmt.Errorf("codetools: clean temps: %w", err)
		}
		for _, p := range found {
			if os.Remove(p) == nil {
				removed++
			}
		}
	}
	return removed, nil
}
