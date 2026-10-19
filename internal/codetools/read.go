package codetools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// read_file limits (CODE-TOOLS §4, CT-32, CT-34).
const (
	readDefaultLimit = 400
	readMaxLimit     = 2000
	lineMaxChars     = 2000
	inMemoryMax      = 64 << 20 // UTF-16 decode and string edits (§5.4)
)

// ReadArgs are the read_file arguments (CODE-TOOLS §4.1).
type ReadArgs struct {
	Path    string `json:"path"`
	Offset  *int   `json:"offset,omitempty"` // 1-based; negative counts from the end
	Limit   int    `json:"limit,omitempty"`
	Outline bool   `json:"outline,omitempty"`
}

// Read runs read_file.
func (e *Engine) Read(ctx context.Context, a ReadArgs, o Opts) (res Result, err error) {
	off, limit := 1, a.Limit
	if a.Offset != nil {
		off = *a.Offset
	}
	switch {
	case a.Path == "":
		return res, toolErr(CodeInvalidArgs, `read_file requires {"path": string}`, "/path: required")
	case off == 0:
		return res, toolErr(CodeInvalidArgs, "offset is 1-based; negative reads from the end", "/offset: 0 is not a line")
	case limit < 0 || limit > readMaxLimit:
		return res, toolErr(CodeInvalidArgs, "", "/limit: must be 1..%d", readMaxLimit)
	case limit == 0:
		limit = readDefaultLimit
	}
	t, err := e.open(ctx, a.Path, os.O_RDONLY, 0, o)
	if err != nil {
		return res, err
	}
	defer closeInto(&err, t.f)
	fi, err := t.f.Stat()
	if err != nil {
		return res, &ToolError{Code: CodeInternal, Message: "stat " + a.Path, Err: err}
	}
	if fi.IsDir() {
		return res, toolErr(CodeInvalidArgs, "use glob to list a directory", "%s is a directory", a.Path)
	}
	return e.readText(&fileView{t: t, key: keyOf(t.resolved, fi)}, off, limit, o)
}

// fileView is an opened file with its sniffed head.
type fileView struct {
	t   *target
	key fileKey
}

func sumTag(s [32]byte) string { return hex.EncodeToString(s[:4]) }

// remember records a tag handed to the model (CODE-TOOLS §5.1).
func (e *Engine) remember(resolved, tag string, ent *tagEntry) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cache.tags[tagKey(resolved, tag)] = *ent
}

// loadAll reads the whole file (≤ inMemoryMax) with its sha256.
func (e *Engine) loadAll(f *fileView) ([]byte, [32]byte, error) {
	if f.key.size > inMemoryMax {
		return nil, [32]byte{}, toolErr(CodeTooLarge, "use offset/limit pages or line-range edits", "%s is larger than %d MiB", f.t.rel, inMemoryMax>>20)
	}
	// Read to EOF, not to the stat size: pseudo-files (/proc) report 0.
	var b bytes.Buffer
	b.Grow(int(f.key.size) + bytes.MinRead)
	if _, err := b.ReadFrom(io.LimitReader(io.NewSectionReader(f.t.f, 0, inMemoryMax+1), inMemoryMax+1)); err != nil {
		return nil, [32]byte{}, &ToolError{Code: CodeInternal, Message: "read " + f.t.rel, Err: err}
	}
	if b.Len() > inMemoryMax {
		return nil, [32]byte{}, toolErr(CodeTooLarge, "use offset/limit pages or line-range edits", "%s is larger than %d MiB", f.t.rel, inMemoryMax>>20)
	}
	data := b.Bytes()
	return data, sha256.Sum256(data), nil
}

func (e *Engine) readText(f *fileView, off, limit int, o Opts) (Result, error) {
	raw, sum, err := e.loadAll(f)
	if err != nil {
		return Result{}, err
	}
	tag, ent := sumTag(sum), tagEntry{sum: sum, stat: f.key, at: time.Now()}
	e.remember(f.t.resolved, tag, &ent)
	lines := splitLines(raw)
	total := len(lines)
	start := off
	if off < 0 {
		start = max(1, total+off+1)
	}
	if total > 0 && start > total {
		return Result{}, toolErr(CodeInvalidArgs, "", "/offset: %d is past the end (%d lines)", off, total)
	}
	pw := newPage(o.pageChars())
	for n := start; n <= total && n < start+limit; n++ {
		if !pw.add(strconv.Itoa(n), trimEOL(lines[n-1])) {
			break
		}
	}
	next := 0
	if last := start + pw.count - 1; last < total {
		next = last + 1
	}
	hdr := header{rel: f.t.rel, tag: tag, first: start, count: pw.count, total: total}
	return pw.result(e, f.t, &hdr, next), nil
}

// splitLines splits text after each '\n'; a final unterminated line counts.
func splitLines(text []byte) [][]byte {
	lines := make([][]byte, 0, bytes.Count(text, []byte{'\n'})+1)
	for len(text) > 0 {
		i := bytes.IndexByte(text, '\n')
		if i < 0 {
			lines = append(lines, text)
			break
		}
		lines = append(lines, text[:i+1])
		text = text[i+1:]
	}
	return lines
}

func trimEOL(l []byte) []byte {
	return bytes.TrimSuffix(bytes.TrimSuffix(l, []byte{'\n'}), []byte{'\r'})
}

// header is the first line of a read_file page (CODE-TOOLS §9.2).
type header struct {
	rel, tag            string
	first, count, total int
	invalid             int
}

// page accumulates "N|line" rows up to a char budget (ADR-031).
type page struct {
	b       strings.Builder
	budget  int
	count   int
	invalid int
}

func newPage(chars int) *page { return &page{budget: chars - 160} }

// add appends one row unless it would exceed the budget (the first row is
// always added: a row is ≤ lineMaxChars + suffix, well under any cap).
func (p *page) add(num string, line []byte) bool { return p.addCut(num, line, 0) }

// addCut is add for a line whose last cut chars were already dropped.
func (p *page) addCut(num string, line []byte, cut int) bool {
	line, k := escapeInvalid(line)
	p.invalid += k
	if utf8.RuneCount(line) > lineMaxChars {
		i := 0
		for range lineMaxChars {
			_, size := utf8.DecodeRune(line[i:])
			i += size
		}
		cut += utf8.RuneCount(line[i:])
		line = line[:i]
	}
	need := len(num) + 2 + utf8.RuneCount(line)
	if p.count > 0 && p.budget-need < 0 {
		return false
	}
	p.budget -= need
	p.b.WriteString(num)
	p.b.WriteByte('|')
	p.b.Write(line)
	if cut > 0 {
		fmt.Fprintf(&p.b, "… [+%d chars]", cut)
	}
	p.b.WriteByte('\n')
	p.count++
	return true
}

func (p *page) result(e *Engine, t *target, h *header, next int) Result {
	h.invalid = p.invalid
	var b strings.Builder
	last := h.first + h.count - 1
	if h.count == 0 {
		last = h.first - 1
	}
	fmt.Fprintf(&b, "%s #%s · L%d-%d/%d", h.rel, h.tag, h.first, last, h.total)
	if h.invalid > 0 {
		fmt.Fprintf(&b, " · invalid_utf8=%d", h.invalid)
	}
	b.WriteByte('\n')
	b.WriteString(p.b.String())
	meta := map[string]any{"tag": h.tag, "lines_total": h.total}
	if next != 0 {
		fmt.Fprintf(&b, "next: offset=%d\n", next)
		meta["next"] = next
	}
	return Result{Content: e.wrapOutside(t, strings.TrimSuffix(b.String(), "\n")), Meta: meta}
}

// wrapOutside marks content read outside the workspace as untrusted
// (CODE-TOOLS §8, ADR-069, ADR-070).
func (e *Engine) wrapOutside(t *target, s string) string {
	if !t.outside {
		return s
	}
	return "<untrusted source=\"file:outside_workspace\">\n" + s + "\n</untrusted>"
}
