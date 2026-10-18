package codetools

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

// pathLock serializes edits of one resolved path inside this process.
func (e *Engine) pathLock(p string) func() {
	e.mu.Lock()
	m, ok := e.locks[p]
	if !ok {
		m = &sync.Mutex{}
		e.locks[p] = m
	}
	e.touch()
	e.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// current is a target opened for an edit with its verified bytes.
type current struct {
	t   *target
	fi  os.FileInfo
	key fileKey
	raw []byte // nil above inMemoryMax
	tag string
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
	if known && ent.sum == sum && e.provenUnchanged(c, &ent) {
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

// docLines returns the logical lines of raw (for change summaries).
func docLines(raw []byte) []string {
	d, err := parseDoc(raw)
	if err != nil {
		return strings.Split(string(raw), "\n")
	}
	out := make([]string, len(d.lines))
	for i, l := range d.lines {
		out[i] = l.content
	}
	return out
}

// forget drops every tag of a deleted or renamed path (§5.5).
func (e *Engine) forget(p string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for k := range e.cache.tags {
		if strings.HasPrefix(k, p+"\x00") {
			delete(e.cache.tags, k)
		}
	}
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
	unlock := e.pathLock(e.abs(a.Path))
	defer unlock()
	c, err := e.openCurrent(ctx, a.Path, a.Tag, o)
	if c != nil {
		defer closeInto(&err, c.t.f)
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
	ci, err := e.commit(ctx, c.t, c.key, c.fi.Mode().Perm(), out)
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
	a, b := make([]string, len(d.lines)), make([]string, len(lines))
	for i, l := range d.lines {
		a[i] = l.content
	}
	for i, l := range lines {
		b[i] = l.content
	}
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
	verb := "edited"
	if c.tag == "" {
		verb = "created"
	}
	head := fmt.Sprintf("%s %s #%s→#%s · %s · +%d -%d", verb, c.t.rel, c.tag, tag, what, add, del)
	if c.tag == "" {
		head = fmt.Sprintf("%s %s #%s · %d lines", verb, c.t.rel, tag, len(lines))
		body = ""
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

// Write runs write_file (CODE-TOOLS §5.6).
func (e *Engine) Write(ctx context.Context, a WriteArgs, o Opts) (res Result, err error) {
	switch {
	case a.Path == "":
		return res, toolErr(CodeInvalidArgs, "", "/path: required")
	case utf8.RuneCountInString(a.Content) > maxWriteChars:
		return res, toolErr(CodeInvalidArgs, "split the file or use edit_file", "/content: more than %d chars", maxWriteChars)
	case strings.Contains(a.Content, sentinel):
		return res, toolErr(CodeInvalidArgs, "secret redacted; edit around it", "/content: contains a redaction sentinel")
	case a.Tag != nil && !reTag.MatchString(*a.Tag):
		return res, toolErr(CodeInvalidArgs, "", "/tag: must match ^[0-9a-f]{8}$")
	}
	abs := e.abs(a.Path)
	unlock := e.pathLock(abs)
	defer unlock()
	if _, lerr := os.Lstat(abs); errors.Is(lerr, fs.ErrNotExist) {
		return e.create(ctx, a, abs, o)
	}
	if a.Tag == nil {
		return res, toolErr(CodeStaleRead, "read_file first; overwriting needs its tag", "%s exists", a.Path)
	}
	c, err := e.openCurrent(ctx, a.Path, *a.Tag, o)
	if c != nil {
		defer closeInto(&err, c.t.f)
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
	ci, err := e.commit(ctx, c.t, c.key, c.fi.Mode().Perm(), out)
	if err != nil {
		return res, err
	}
	return e.changed(c, d, lines, out, &ci, "write", 1), nil
}

// create writes a new file: parents created inside the sandbox, UTF-8
// without BOM, files.new_eol line endings (CODE-TOOLS §5.6).
func (e *Engine) create(ctx context.Context, a WriteArgs, abs string, o Opts) (Result, error) {
	if a.Tag != nil {
		return Result{}, toolErr(CodeNotFound, "omit tag to create a new file", "%s does not exist", a.Path)
	}
	v, resolved, err := e.sb.Check(ctx, abs, sandbox.OpWrite)
	if err != nil {
		return Result{}, mapOpenErr(a.Path, err)
	}
	outside := v != sandbox.Inside
	if outside && !o.Approved {
		return Result{}, toolErr(CodeOutsideWorkspace, "paths outside the workspace need approval", "%s is outside the workspace", a.Path)
	}
	if merr := e.mkdirs(ctx, filepath.Dir(resolved), outside); merr != nil {
		return Result{}, merr
	}
	t := &target{resolved: resolved, rel: e.rel(resolved), outside: outside}
	d := &doc{}
	lines := splitDocLines(a.Content, e.newEOL)
	out, _ := d.render(lines)
	ci, err := e.commit(ctx, t, fileKey{}, 0, out)
	if err != nil {
		return Result{}, err
	}
	res := e.changed(&current{t: t}, d, lines, out, &ci, "write", 1)
	res.Meta["created"] = true
	return res, nil
}
