package codetools

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/sandbox"
)

// write_file content cap (CODE-TOOLS §5.6).
const maxWriteChars = 1_000_000

// reTag is the tag argument shape (^[0-9a-f]{8}$).
var reTag = regexp.MustCompile(`^[0-9a-f]{8}$`)

// EditArgs are the edit_file arguments (CODE-TOOLS §5.2).
type EditArgs struct {
	Path  string     `json:"path"`
	Tag   string     `json:"tag"`
	Edits []EditItem `json:"edits"`
}

// WriteArgs are the write_file arguments (CODE-TOOLS §5.6).
type WriteArgs struct {
	Path    string  `json:"path"`
	Content string  `json:"content"`
	Tag     *string `json:"tag,omitempty"`
}

// pathMu is one per-file edit lock; n counts holders and waiters.
type pathMu struct {
	sync.Mutex
	n int
}

// pathLock serializes edits of one file inside this process (CT27-5). Two
// keys are taken: the symlink-resolved path, which keeps one name
// serialized even while a commit replaces the object, and the OS identity
// of the file (device+inode; volume serial+file index on Windows), which
// makes every alias of one object — symlinks, hard links, case variants —
// share the lock. Distinct files never share either key, whatever their
// size, mtime or creation tick. A target that does not exist yet has only
// a path key. An identity that cannot be read fails closed: no size+mtime
// substitute is used. Unused locks are dropped.
func (e *Engine) pathLock(ctx context.Context, p string) (func(), error) {
	keys, err := e.lockKeys(ctx, p)
	if err != nil {
		return nil, err
	}
	ms := make([]*pathMu, len(keys))
	for i, key := range keys {
		e.mu.Lock()
		m := e.locks[key]
		if m == nil {
			m = &pathMu{}
			e.locks[key] = m
		}
		m.n++
		e.mu.Unlock()
		ms[i] = m
		m.Lock()
	}
	e.touch()
	return func() {
		for i, m := range ms {
			m.Unlock()
			e.mu.Lock()
			if m.n--; m.n == 0 {
				delete(e.locks, keys[i])
			}
			e.mu.Unlock()
		}
	}, nil
}

// lockKeys returns the lock keys of p in acquisition order: its resolved
// path first, then, for a file the sandbox has classified inside a root,
// its OS identity. That fixed order keeps two pathLock calls sharing a key
// from deadlocking. When the sandbox cannot classify p the lexical path
// stands in (the operation fails later at open); a present file whose
// identity read fails other than by absence returns IdentityError, so no
// stat tuple stands in.
func (e *Engine) lockKeys(ctx context.Context, p string) ([]string, error) {
	keys := []string{e.abs(p)}
	if v, r, err := e.sb.Check(ctx, keys[0], sandbox.OpRead); err == nil {
		keys[0] = r
		if id, ierr := fileIdentity(v, r); ierr == nil {
			keys = append(keys, fmt.Sprintf("\x00%d:%d:%d", id.Vol, id.Hi, id.Lo))
		} else if !errors.Is(ierr, fs.ErrNotExist) {
			return nil, ierr
		}
	}
	if e.fold || runtime.GOOS == "darwin" {
		for i := range keys {
			keys[i] = strings.ToLower(keys[i])
		}
	}
	return keys, nil
}

// fileIdentity returns the identity of sandbox-classified path r, or the
// zero fileID when v says r is outside every root (no handle is opened
// outside the sandbox).
func fileIdentity(v sandbox.Verdict, r string) (fileID, error) {
	if v != sandbox.Inside && v != sandbox.HardLinked {
		return fileID{}, nil
	}
	id, err := identify(r)
	if err == nil && id == (fileID{}) {
		return fileID{}, &IdentityError{Path: r, Err: errors.New("zero file identity")}
	}
	return id, err
}

// current is a target opened for an edit with its verified bytes.
type current struct {
	t   *target
	fi  os.FileInfo
	key fileKey
	raw []byte // nil above inMemoryMax
	tag string
}

// checkLease rejects a write leased by another child (GAP-TOOL-05), under the
// per-path lock.
func (e *Engine) checkLease(ctx context.Context, p string) error {
	if e.guard == nil {
		return nil
	}
	owner, leased, err := e.guard.LeasedBy(ctx, p)
	if err != nil {
		return &ToolError{Code: CodeInternal, Message: "lease check failed", Err: err}
	}
	if !leased {
		return nil
	}
	return toolErr(CodeConflict, "edit another file or wait for the owner", "%s is leased by delegate %s", p, owner)
}

// openCurrent opens path and checks tag (CODE-TOOLS §5.1, §5.3 butir 1-2):
// unknown tag or changed content → stale_read with the fresh tag (which
// becomes valid) and the changed line ranges.
func (e *Engine) openCurrent(ctx context.Context, p, tag string, o Opts) (*current, error) {
	t, err := e.open(ctx, p, os.O_RDONLY, 0, o)
	if codeOf(err) == CodeNotFound {
		e.forget(e.abs(p))
	}
	if err != nil {
		return nil, err
	}
	c := &current{t: t}
	if c.fi, err = t.f.Stat(); err != nil {
		return c, ioErr("stat", t.rel, err)
	}
	if !c.fi.Mode().IsRegular() {
		return c, toolErr(CodeInvalidArgs, "", "%s is not a regular file", p)
	}
	c.key = keyOf(t.resolved, c.fi)
	e.mu.Lock()
	ent, known := e.cache.tags[tagKey(t.resolved, tag)]
	snap, hasSnap := e.cache.snaps.get(tagKey(t.resolved, tag))
	e.mu.Unlock()
	sum, err := e.currentSum(c)
	if err != nil {
		return c, err
	}
	if known && ent.sum == sum && (c.t.client != nil || e.provenUnchanged(c, &ent)) {
		return c, nil
	}
	fresh := &tagEntry{sum: sum, stat: c.key, at: time.Now()}
	if c.key.size > fullHashMax {
		if fresh, err = statEntry(t.f, c.key); err != nil {
			return c, ioErr("hash", t.rel, err)
		}
	}
	e.remember(t.resolved, c.tag, fresh, c.raw)
	if !known {
		return c, toolErr(CodeStaleRead, "read the file first (read_file or grep give a tag)", "%s: tag %s is not current for this session (now #%s)", t.rel, tag, c.tag)
	}
	if c.t.client != nil {
		return c, toolErr(CodeStaleRead, "read_file returns the current tag", "%s: stale_tag; the client buffer changed (now #%s)", t.rel, c.tag)
	}
	return c, staleErr(c, tag, snap, hasSnap)
}

// currentSum loads files ≤ inMemoryMax and returns the digest the tag
// rule compares: content sha256 ≤ fullHashMax, stat digest above.
func (e *Engine) currentSum(c *current) ([32]byte, error) {
	var sum [32]byte
	if c.key.size <= inMemoryMax {
		var err error
		if c.raw, sum, err = e.loadAll(&fileView{t: c.t, key: c.key}); err != nil {
			return sum, err
		}
	}
	if c.key.size <= fullHashMax {
		c.tag = sumTag(sum)
		return sum, nil
	}
	c.tag = statTag(c.key)
	return statSum(c.key), nil
}

// provenUnchanged: a racy stat-tag (mtime within racyWindow of the tag)
// needs the content hash recorded with it to match the file now (§5.5);
// without one the edit is stale.
func (e *Engine) provenUnchanged(c *current, ent *tagEntry) bool {
	if c.key.size <= fullHashMax || !ent.racy() {
		return true
	}
	full, err := hashFile(c.t.f, c.key.size)
	return err == nil && ent.hasFull && full == ent.full
}

// staleErr reports the changed line ranges since the tag (§5.3 butir 2).
func staleErr(c *current, tag string, snap []byte, hasSnap bool) *ToolError {
	changes, first := "changes: unknown", ""
	if hasSnap && c.raw != nil {
		changes, first = changedRanges(docLines(snap), docLines(c.raw))
	}
	hint := fmt.Sprintf("string edits may retry with tag %s; line edits must re-read", c.tag)
	if first != "" {
		hint += " " + first
	}
	return toolErr(CodeStaleRead, hint, "%s changed since #%s (now #%s): %s", c.t.rel, tag, c.tag, changes)
}

func docLines(raw []byte) []string {
	d, err := parseDoc(raw)
	if err != nil {
		return strings.Split(string(raw), "\n")
	}
	return contents(d.lines)
}

func contents(ls []docLine) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.content
	}
	return out
}

// forget drops every tag of a deleted or renamed path (§5.5).
func (e *Engine) forget(p string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	maps.DeleteFunc(e.cache.tags, func(k string, _ tagEntry) bool { return strings.HasPrefix(k, p+"\x00") })
}

// Edit runs edit_file (CODE-TOOLS §5.3).
func (e *Engine) Edit(ctx context.Context, a EditArgs, o Opts) (res Result, err error) {
	switch {
	case a.Path == "":
		return res, toolErr(CodeInvalidArgs, "", "/path: required")
	case !reTag.MatchString(a.Tag):
		return res, toolErr(CodeInvalidArgs, "read_file returns the tag", "/tag: must match ^[0-9a-f]{8}$")
	case len(a.Edits) == 0 || len(a.Edits) > maxEdits:
		return res, toolErr(CodeInvalidArgs, "", "/edits: 1..%d items", maxEdits)
	}
	unlock, lerr := e.pathLock(ctx, a.Path)
	if lerr != nil {
		return res, lerr
	}
	defer unlock()
	if cerr := e.checkLease(ctx, a.Path); cerr != nil {
		return res, cerr
	}
	o.local, err = e.sourceLocal(ctx, true)
	if err != nil {
		return res, err
	}
	c, err := e.openCurrent(ctx, a.Path, a.Tag, o)
	if c != nil {
		defer c.t.close(&err)
	}
	if err != nil {
		return res, err
	}
	if c.raw == nil {
		return e.editLarge(ctx, c, a.Edits)
	}
	d, terr := parseDoc(c.raw)
	if terr != nil {
		return res, terr
	}
	spans, terr := resolveEdits(d, c.t.rel, a.Edits)
	if terr != nil {
		return res, terr
	}
	lines := d.apply(spans)
	out, ok := d.render(lines)
	if !ok {
		return res, toolErr(CodeInvalidArgs, "", "%s: result is not representable in %s", c.t.rel, d.enc)
	}
	ci, err := e.commit(ctx, c.t, c.key, keepMode(c.fi), out)
	if err != nil {
		return res, err
	}
	return e.changed(c, d, lines, out, &ci, fmt.Sprintf("%d edit", len(a.Edits)), len(spans)), nil
}

// changed records the new tag and renders "edited path #a→#b · … · +x -y"
// with the unified diff (CODE-TOOLS §9.4).
func (e *Engine) changed(c *current, d *doc, lines []docLine, out []byte, ci *commitInfo, what string, repl int) Result {
	tag := sumTag(ci.sum)
	ent := &tagEntry{sum: ci.sum, stat: ci.key, at: time.Now()}
	if len(out) > fullHashMax {
		tag = statTag(ci.key)
		ent.sum, ent.full, ent.hasFull = statSum(ci.key), ci.sum, true
	}
	e.remember(c.t.resolved, tag, ent, out)
	if len(out) <= readCacheFile {
		e.mu.Lock()
		e.cache.read.put(ci.key, readEntry{sum: sha256.Sum256(out), data: out, at: time.Now()}, len(out)+64)
		e.mu.Unlock()
	}
	a, b := contents(d.lines), contents(lines)
	ops, ok := lineDiff(a, b)
	add, del := diffStat(ops)
	body := ""
	if ok {
		body = unified(ops, a, b)
	} else {
		add, del, body = len(b), len(a), "(diff omitted: too many changes)"
	}
	if strings.HasSuffix(what, " edit") && !strings.HasPrefix(what, "1 ") {
		what += "s"
	}
	head := fmt.Sprintf("edited %s #%s→#%s · %s · +%d -%d", c.t.rel, c.tag, tag, what, add, del)
	if c.tag == "" {
		head, body = fmt.Sprintf("created %s #%s · %d lines", c.t.rel, tag, len(lines)), ""
	}
	meta := map[string]any{
		"tag": tag, "replacements": repl, "diff_stat": fmt.Sprintf("+%d -%d", add, del),
		"eol": eolName(d.dominantEOL() + map[bool]string{true: e.newEOL}[c.tag == ""]), "encoding": d.enc.String(), "bytes": len(out),
		"owner_preserved": ci.ownerOK, "mode_preserved": ci.modeOK, "xattr_preserved": ci.xattrOK,
	}
	if ci.inplace {
		meta["inplace"] = "hardlink"
	}
	if body != "" {
		head += "\n" + body
	}
	return Result{Content: e.wrapOutside(c.t, head), Meta: meta}
}

func eolName(s string) string {
	if strings.HasSuffix(s, "\r\n") {
		return "crlf"
	}
	return "lf"
}

// validateWrite checks write_file arguments before any I/O.
func validateWrite(a WriteArgs) *ToolError {
	switch {
	case a.Path == "":
		return toolErr(CodeInvalidArgs, "", "/path: required")
	case utf8.RuneCountInString(a.Content) > maxWriteChars:
		return toolErr(CodeInvalidArgs, "split the file or use edit_file", "/content: more than %d chars", maxWriteChars)
	case strings.Contains(a.Content, sentinel):
		return toolErr(CodeInvalidArgs, "secret redacted; edit around it", "/content: contains a redaction sentinel")
	case a.Tag != nil && !reTag.MatchString(*a.Tag):
		return toolErr(CodeInvalidArgs, "", "/tag: must match ^[0-9a-f]{8}$")
	}
	return nil
}

// Write runs write_file (CODE-TOOLS §5.6).
func (e *Engine) Write(ctx context.Context, a WriteArgs, o Opts) (res Result, err error) {
	if terr := validateWrite(a); terr != nil {
		return res, terr
	}
	abs := e.abs(a.Path)
	unlock, lerr := e.pathLock(ctx, a.Path)
	if lerr != nil {
		return res, lerr
	}
	defer unlock()
	if cerr := e.checkLease(ctx, a.Path); cerr != nil {
		return res, cerr
	}
	o.local, lerr = e.sourceLocal(ctx, true)
	if lerr != nil {
		return res, lerr
	}
	existing, xerr := e.writeTargetExists(ctx, abs, o)
	if xerr != nil {
		return res, xerr
	}
	if !existing {
		return e.create(ctx, a, abs, o)
	}
	if a.Tag == nil {
		return res, toolErr(CodeStaleRead, "read_file first; overwriting needs its tag", "%s exists", a.Path)
	}
	c, err := e.openCurrent(ctx, a.Path, *a.Tag, o)
	if c != nil {
		defer c.t.close(&err)
	}
	if err != nil {
		return res, err
	}
	if c.raw == nil {
		return res, toolErr(CodeTooLarge, "use edit_file line ranges", "%s is larger than %d MiB", c.t.rel, inMemoryMax>>20)
	}
	d, terr := parseDoc(c.raw)
	if terr != nil {
		return res, terr
	}
	lines := splitDocLines(a.Content, d.dominantEOL())
	out, ok := d.render(lines)
	if !ok {
		return res, toolErr(CodeInvalidArgs, "", "%s: content is not representable in %s", c.t.rel, d.enc)
	}
	ci, err := e.commit(ctx, c.t, c.key, keepMode(c.fi), out)
	if err != nil {
		return res, err
	}
	return e.changed(c, d, lines, out, &ci, "write", 1), nil
}

// writeTargetExists reports whether the write target exists; with a client
// source the buffer decides, otherwise the disk does.
func (e *Engine) writeTargetExists(ctx context.Context, abs string, o Opts) (bool, error) {
	if e.client != nil && !o.local {
		_, notFound, rerr := e.client.ReadTextFile(ctx, abs)
		if rerr != nil {
			return false, ioErr("client read", e.rel(abs), rerr)
		}
		return !notFound, nil
	}
	_, err := os.Lstat(abs)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	return false, nil
}

// create writes a new file: parents created inside the sandbox, UTF-8
// without BOM, files.new_eol line endings (CODE-TOOLS §5.6). A tag is only a
// snapshot of an existing file, so a tag supplied for a missing path is
// ignored: there is no data to clobber and the model's natural call shape
// sends a placeholder tag (ADR-116).
func (e *Engine) create(ctx context.Context, a WriteArgs, abs string, o Opts) (Result, error) {
	v, resolved, err := e.sb.Check(ctx, abs, sandbox.OpWrite)
	if err != nil {
		return Result{}, mapOpenErr(a.Path, err)
	}
	outside := v != sandbox.Inside
	if aerr := approval(v, a.Path, o.Approved); aerr != nil {
		return Result{}, aerr
	}
	if e.client == nil {
		if merr := e.mkdirs(ctx, filepath.Dir(resolved), outside); merr != nil {
			return Result{}, merr
		}
	}
	t := &target{resolved: resolved, rel: e.rel(resolved), outside: outside, journal: o.Journal}
	if e.client != nil && !o.local {
		t.client = e.client
	}
	d := &doc{}
	lines := splitDocLines(a.Content, e.newEOL)
	out, _ := d.render(lines)
	ci, err := e.commit(ctx, t, fileKey{}, 0, out)
	if err != nil {
		return Result{}, err
	}
	res := e.changed(&current{t: t}, d, lines, out, &ci, "write", 1)
	res.Meta["created"] = true
	if a.Tag != nil {
		res.Meta["tag_ignored"] = true
		res.Content += "\n[tag ignored: " + a.Path + " did not exist; created a new file]"
	}
	return res, nil
}
