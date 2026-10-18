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
	"time"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/sandbox"
)

// write_file content cap (CODE-TOOLS §5.6).
const maxWriteChars = 1_000_000

// reTag is the tag argument shape (^[0-9a-f]{8}$).
var reTag = regexp.MustCompile(`^[0-9a-f]{8}$`)

// WriteArgs are the write_file arguments (CODE-TOOLS §5.6).
type WriteArgs struct {
	Path    string  `json:"path"`
	Content string  `json:"content"`
	Tag     *string `json:"tag,omitempty"`
}

// current is a target opened for an edit with its verified bytes.
type current struct {
	t   *target
	tag string
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
	head := fmt.Sprintf("edited %s #%s→#%s · %s", c.t.rel, c.tag, tag, what)
	if c.tag == "" {
		head = fmt.Sprintf("created %s #%s · %d lines", c.t.rel, tag, len(lines))
	}
	meta := map[string]any{
		"tag": tag, "replacements": repl,
		"eol": eolName(d.dominantEOL() + map[bool]string{true: e.newEOL}[c.tag == ""]), "encoding": d.enc.String(), "bytes": len(out),
		"mode_preserved": ci.modeOK,
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
	if _, lerr := os.Lstat(abs); errors.Is(lerr, fs.ErrNotExist) {
		return e.create(ctx, a, abs, o)
	}
	return res, toolErr(CodeStaleRead, "read_file first; overwriting needs its tag", "%s exists", a.Path)
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
	if aerr := approval(v, a.Path, o.Approved); aerr != nil {
		return Result{}, aerr
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
