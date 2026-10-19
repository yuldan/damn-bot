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
	"regexp"
	"slices"
	"strings"

	"github.com/damnbot/damnbot/internal/sandbox"
)

// tempMarker names the atomic-write temp files: ".<name>.damnbot-<rand>"
// (CODE-TOOLS §5.4), removed when orphaned by CleanOrphanTemps; reOrphan
// matches exactly those names (CT27-10), not the in-place backups
// ".<name>.damnbot-bak-<rand>", which a crash leaves for recovery.
const tempMarker = ".damnbot-"

var reOrphan = regexp.MustCompile(`^\..+\.damnbot-[a-z2-7]{12}$`)

// commitInfo is what the write path reports in Meta (P14).
type commitInfo struct {
	ownerOK, modeOK, xattrOK bool
	sum                      [32]byte
	key                      fileKey
}

// commit replaces t's file with data (CODE-TOOLS §5.4): temp in the
// resolved directory, mode set before the first byte, fsync(file), owner
// and xattrs copied, re-stat against before (stale_read if changed),
// rename + fsync(dir), then a read-back sha256 check (TOOL-03). mode 0
// creates a new file with the umask-derived mode. On any
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
// *ToolError. The new bytes always go to a complete, fsynced temp first,
// so a failed fill (validation, no-op, disk full) never touches the target.
func (e *Engine) commitStream(ctx context.Context, t *target, before fileKey, mode fs.FileMode, fill func(io.Writer) error) (ci commitInfo, err error) {
	ci = commitInfo{ownerOK: true, modeOK: true, xattrOK: true}
	tmp, tmpName, err := e.createTemp(ctx, t, "")
	if err != nil {
		return ci, err
	}
	renamed := false
	defer func() { discardTemp(tmp, tmpName, renamed) }()
	prepareTemp(t, before, mode, tmp, &ci)
	if werr := hashFill(e.hooked(tmp, "mid-temp"), fill, &ci); werr != nil {
		return ci, fillErr("write", t.rel, werr)
	}
	if werr := tmp.Sync(); werr != nil {
		return ci, ioErr("fsync", t.rel, werr)
	}
	if werr := e.at("pre-rename"); werr != nil {
		return ci, ioErr("rename", t.rel, werr)
	}
	var cerr error
	t.close(&cerr) // Windows cannot replace a file that is still open
	if werr := errors.Join(cerr, e.unchanged(t, before)); werr != nil {
		return ci, werr
	}
	werr := tmp.Close()
	tmp = nil
	if werr != nil {
		return ci, ioErr("close", t.rel, werr)
	}
	if werr := renameReplace(tmpName, t.resolved); werr != nil {
		return ci, ioErr("rename", t.rel, werr)
	}
	renamed = true
	if werr := errors.Join(e.at("post-rename"), e.syncDir(ctx, filepath.Dir(t.resolved), t.outside)); werr != nil {
		return ci, ioErr("fsync dir", t.rel, werr)
	}
	err = e.readBack(ctx, t, &ci)
	return ci, err
}

// prepareTemp copies owner and xattrs first (xattr writes need a writable
// inode; chown clears setuid/setgid), then sets the final mode before the
// first byte (§5.4 langkah 2).
func prepareTemp(t *target, before fileKey, mode fs.FileMode, tmp *os.File, ci *commitInfo) {
	if t.f != nil {
		ci.ownerOK = copyOwner(t.f, tmp)
		ci.xattrOK = copyXattrs(t.f, tmp)
	}
	if before != (fileKey{}) {
		ci.modeOK = setMode(tmp, mode)
	}
}

// createTemp creates ".<name>.damnbot-<kind><rand>" next to the target,
// through the sandbox (O_EXCL, mode 0o600; 0o666 minus umask for a new
// file, whose mode is not set afterwards).
func (e *Engine) createTemp(ctx context.Context, t *target, kind string) (*os.File, string, error) {
	perm := fs.FileMode(0o600)
	if t.f == nil {
		perm = 0o666
	}
	name := filepath.Join(filepath.Dir(t.resolved), "."+filepath.Base(t.resolved)+tempMarker+kind+randHex())
	f, _, err := e.openPath(ctx, name, e.rel(name), os.O_RDWR|os.O_CREATE|os.O_EXCL, perm, t.outside)
	return f, name, err
}

// setMode sets mode on f and reports whether it holds afterwards (the
// kernel may drop setgid for a group the process is not in).
func setMode(f *os.File, mode fs.FileMode) bool {
	if f.Chmod(mode) != nil {
		return false
	}
	fi, err := f.Stat()
	return err == nil && keepMode(fi) == mode
}

// at fires the test hook at a named commit point (nil in production).
func (e *Engine) at(point string) error {
	if e.hook == nil {
		return nil
	}
	return e.hook(point)
}

// hooked returns w, or with a test hook set, a writer that fires point
// once between the two halves of the first write: a CT-25 kill point or
// injected failure inside a partially written file.
func (e *Engine) hooked(w io.Writer, point string) io.Writer {
	if e.hook == nil {
		return w
	}
	return &hookWriter{w: w, fire: func() error { return e.hook(point) }}
}

type hookWriter struct {
	w    io.Writer
	fire func() error
	done bool
}

func (h *hookWriter) Write(p []byte) (int, error) {
	k := len(p)
	if !h.done && k > 1 {
		k /= 2
	}
	n, err := h.w.Write(p[:k])
	if err == nil && k < len(p) {
		h.done = true
		if err = h.fire(); err == nil {
			var m int
			m, err = h.w.Write(p[k:])
			n += m
		}
	}
	if err != nil {
		return n, fmt.Errorf("codetools: write: %w", err)
	}
	return n, nil
}

// syncDir fsyncs the directory of a renamed file, opened through the sandbox.
func (e *Engine) syncDir(ctx context.Context, dir string, outside bool) error {
	if !dirSyncable {
		return nil
	}
	d, err := e.openResolved(ctx, dir, outside)
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
	f, err := e.openResolved(ctx, t.resolved, t.outside)
	if err != nil {
		return err
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
		if err != nil {
			return mapOpenErr(e.rel(d), err)
		}
		if err := approval(v, e.rel(d), approved); err != nil {
			return err
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

// keepMode is the mode an edit gives the rewritten file: the permission
// bits plus setuid/setgid/sticky (CT27-9).
func keepMode(fi fs.FileInfo) fs.FileMode {
	return fi.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
}

// CleanOrphanTemps removes ".<name>.damnbot-<rand>" files left by a crash
// in the session roots (CODE-TOOLS §5.4), each unlinked relative to its
// directory opened through the sandbox; call it at session start.
func (e *Engine) CleanOrphanTemps(ctx context.Context) (int, error) {
	removed := 0
	for _, root := range e.roots {
		r, err := e.searchRoot(ctx, root, Opts{})
		if err != nil {
			return removed, err
		}
		var found []string
		spec := &walkSpec{root: r.abs, hidden: true, keepFile: func(rel string) bool {
			return reOrphan.MatchString(filepath.Base(rel))
		}}
		if _, err := e.walk(ctx, spec, r.st, r.top, func(j *fileJob, _ scanOut) bool {
			found = append(found, j.abs)
			return true
		}); err != nil {
			return removed, fmt.Errorf("codetools: clean temps: %w", err)
		}
		for _, p := range found {
			if e.removeIn(ctx, p) == nil {
				removed++
			}
		}
	}
	return removed, nil
}

// removeIn unlinks p relative to its directory opened through the sandbox
// (no symlinked ancestor is followed; by path on Windows).
func (e *Engine) removeIn(ctx context.Context, p string) error {
	d, err := e.openResolved(ctx, filepath.Dir(p), false)
	if err != nil {
		return err
	}
	return errors.Join(removeAt(d, filepath.Base(p)), d.Close())
}

// discardTemp closes f (nil once handed off) and removes name unless
// keep. Neither failure is the edit's: the temp is fsynced or abandoned,
// never the only copy of any bytes, and a file left behind is an orphan
// for CleanOrphanTemps (§5.4).
func discardTemp(f *os.File, name string, keep bool) {
	var errs []error
	if f != nil {
		errs = append(errs, f.Close())
	}
	if !keep {
		errs = append(errs, os.Remove(name))
	}
	if err := errors.Join(errs...); err == nil || errors.Is(err, fs.ErrNotExist) {
		return
	}
	// Any other failure is dropped by design (see above).
}
