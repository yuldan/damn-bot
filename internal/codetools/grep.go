package codetools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/damnbot/damnbot/internal/sandbox"
)

// grep limits (CODE-TOOLS §2, CT-30).
const (
	grepDefaultLimit = 100
	grepMaxLimit     = 500
	grepMaxContext   = 5
	grepLineChars    = 500
	grepTimeout      = 30 * time.Second
	inMemoryScan     = 16 << 20 // larger files are streamed in scanChunk steps
	scanChunk        = 1 << 20
	scratchMin       = 64 << 10 // per-worker read buffer: initial size
	scratchMax       = 1 << 20  // and the largest size kept between files
)

// GrepArgs are the grep arguments (CODE-TOOLS §2.1).
type GrepArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
	Glob    string `json:"glob,omitempty"`
	Type    string `json:"type,omitempty"`
	Case    string `json:"case,omitempty"`
	Context int    `json:"context,omitempty"`
	Mode    string `json:"mode,omitempty"`
	Offset  int    `json:"offset,omitempty"`
	// Limit: 0 = default; -1 = unlimited (benchmarks only, CODE-TOOLS §10.1).
	Limit int `json:"limit,omitempty"`
}

// grepMatch is one matching line rendered for output.
type grepMatch struct {
	n    int
	text string
}

// scanOut is the per-file scan result produced on a walker worker.
type scanOut struct {
	skip    skipKind
	matches []grepMatch
	count   int
	ctx     map[int]string // context lines by number
	tag     string
}

type skipKind uint8

const (
	skipNone skipKind = iota
	skipBinary
	skipDenied
	skipUnreadable
	skipLinked // hard-linked file needing approval (S16-7)
)

// langTypes is the built-in `type` table (CODE-TOOLS §2.1).
func langTypes() map[string][]string {
	return map[string][]string{
		"go": {"*.go"}, "py": {"*.py", "*.pyi"}, "js": {"*.js", "*.mjs", "*.cjs", "*.jsx"},
		"ts": {"*.ts", "*.tsx", "*.mts", "*.cts"}, "rust": {"*.rs"}, "c": {"*.c", "*.h"},
		"cpp": {"*.cpp", "*.cc", "*.cxx", "*.hpp", "*.hh", "*.hxx", "*.h"}, "java": {"*.java"},
		"kotlin": {"*.kt", "*.kts"}, "rb": {"*.rb"}, "php": {"*.php"}, "cs": {"*.cs"},
		"swift": {"*.swift"}, "sh": {"*.sh", "*.bash", "*.zsh"}, "md": {"*.md", "*.markdown"},
		"json": {"*.json"}, "yaml": {"*.yaml", "*.yml"}, "toml": {"*.toml"}, "sql": {"*.sql"},
		"html": {"*.html", "*.htm"}, "css": {"*.css", "*.scss", "*.less"}, "lua": {"*.lua"},
		"scala": {"*.scala"}, "zig": {"*.zig"}, "xml": {"*.xml"}, "proto": {"*.proto"},
	}
}

// grepPlan is a validated grep request.
type grepPlan struct {
	a       GrepArgs
	m       *matcher
	perFile int // matches kept per file
	filter  func(rel string) bool
	hidden  bool
}

// checkGrepArgs applies the CODE-TOOLS §2.1 bounds.
func checkGrepArgs(a *GrepArgs) error {
	switch {
	case a.Pattern == "":
		return toolErr(CodeInvalidArgs, `grep requires {"pattern": string}`, "/pattern: required")
	case a.Context < 0 || a.Context > grepMaxContext:
		return toolErr(CodeInvalidArgs, "", "/context: must be 0..%d", grepMaxContext)
	case a.Limit < -1 || a.Limit > grepMaxLimit:
		return toolErr(CodeInvalidArgs, "", "/limit: must be 1..%d", grepMaxLimit)
	case a.Offset < 0:
		return toolErr(CodeInvalidArgs, "", "/offset: must be ≥ 0")
	case !slices.Contains([]string{"", "smart", "sensitive", "insensitive"}, a.Case):
		return toolErr(CodeInvalidArgs, "", "/case: smart|sensitive|insensitive")
	case !slices.Contains([]string{"", "matches", "files", "count"}, a.Mode):
		return toolErr(CodeInvalidArgs, "", "/mode: matches|files|count")
	}
	return nil
}

func planGrep(a *GrepArgs) (*grepPlan, error) {
	if err := checkGrepArgs(a); err != nil {
		return nil, err
	}
	p := &grepPlan{a: *a}
	if p.a.Mode == "" {
		p.a.Mode = "matches"
	}
	if p.a.Limit == 0 {
		p.a.Limit = grepDefaultLimit
	}
	m, err := compileMatcher(a.Pattern, a.Case)
	if err != nil {
		return nil, &ToolError{Code: CodeInvalidArgs, Message: "/pattern: " + err.Error(), Hint: "RE2 syntax", Err: err}
	}
	p.m = m
	switch {
	case p.a.Mode == "files":
		p.perFile = 1
	case p.a.Mode == "count" || p.a.Limit < 0:
		p.perFile = -1
	default:
		p.perFile = p.a.Offset + p.a.Limit + 1
	}
	if p.filter, p.hidden, err = fileFilter(a.Glob, a.Type); err != nil {
		return nil, err
	}
	return p, nil
}

// fileFilter builds the glob/type filter; a glob starting with '.' or
// holding "/." includes hidden entries (CODE-TOOLS §2.3).
func fileFilter(glob, typ string) (func(string) bool, bool, error) {
	var globs []string
	if typ != "" {
		g, ok := langTypes()[typ]
		if !ok {
			names := slices.Sorted(maps.Keys(langTypes()))
			return nil, false, toolErr(CodeInvalidArgs, "valid: "+strings.Join(names, ", "), "/type: unknown type %q", typ)
		}
		globs = g
	}
	if glob != "" && !doublestar.ValidatePattern(glob) {
		return nil, false, toolErr(CodeInvalidArgs, "doublestar syntax: **, {a,b}, [...]", "/glob: invalid pattern %q", glob)
	}
	if glob == "" && globs == nil {
		return nil, false, nil
	}
	return func(rel string) bool {
		base := path.Base(rel)
		if glob != "" && !globMatch(glob, rel, base) {
			return false
		}
		return globs == nil || slices.ContainsFunc(globs, func(g string) bool { return globMatch(g, rel, base) })
	}, glob != "" && (glob[0] == '.' || strings.Contains(glob, "/.")), nil
}

// globMatch applies a doublestar pattern; without '/' it matches the
// basename at any depth (CODE-TOOLS §3).
func globMatch(pattern, rel, base string) bool {
	if !strings.Contains(pattern, "/") {
		ok, err := doublestar.Match(pattern, base)
		return ok && err == nil
	}
	ok, err := doublestar.Match(pattern, rel)
	return ok && err == nil
}

// Grep runs grep.
func (e *Engine) Grep(ctx context.Context, a *GrepArgs, o Opts) (Result, error) {
	p, err := planGrep(a)
	if err != nil {
		return Result{}, err
	}
	e.touch()
	ctx, cancel := context.WithTimeout(ctx, grepTimeout)
	defer cancel()
	r, err := e.searchRoot(ctx, a.Path, o)
	out := &grepOut{p: p, e: e, budget: o.pageChars() - 200}
	if errors.Is(err, context.DeadlineExceeded) {
		return out.result(nil, err, &searchRoot{rel: "."}), nil
	}
	if err != nil {
		return Result{}, err
	}
	scan := func(ctx context.Context, abs string, buf *[]byte) scanOut {
		return e.scanFile(ctx, abs, p, r.outside, buf)
	}
	if !r.dir {
		var buf []byte
		out.add(r.rel, scan(ctx, r.abs, &buf))
		return out.result(nil, ctx.Err(), r), nil
	}
	spec := &walkSpec{root: r.abs, hidden: p.hidden, approved: r.outside, keepFile: p.filter, scan: scan}
	st, err := e.walk(ctx, spec, r.st, r.top, func(j *fileJob, s scanOut) bool { return out.add(r.join(j.rel), s) })
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return Result{}, &ToolError{Code: CodeInternal, Message: "grep walk", Err: err}
	}
	return out.result(st, ctx.Err(), r), nil
}

// searchRoot is a sandbox-checked grep/glob starting point.
type searchRoot struct {
	abs, rel string
	dir      bool
	outside  bool
	st       ignoreStack
	top      string
}

func (r *searchRoot) join(rel string) string {
	if r.rel == "." {
		return rel
	}
	return r.rel + "/" + rel
}

func (e *Engine) searchRoot(ctx context.Context, p string, o Opts) (*searchRoot, error) {
	if p == "" {
		p = "."
	}
	v, resolved, err := e.sb.Check(ctx, e.abs(p), sandbox.OpRead)
	if err != nil {
		return nil, mapOpenErr(p, err)
	}
	if aerr := approval(v, p, o.Approved); aerr != nil {
		return nil, aerr
	}
	fi, err := os.Lstat(resolved)
	if err != nil {
		return nil, mapOpenErr(p, err)
	}
	r := &searchRoot{abs: resolved, rel: e.rel(resolved), dir: fi.IsDir(), outside: v != sandbox.Inside}
	if r.dir {
		root := resolved
		for _, s := range e.roots {
			if sandbox.HasPathPrefix(resolved, s, e.fold) {
				root = s
				break
			}
		}
		r.st, r.top = e.ignoreBase(ctx, root, resolved)
	}
	return r, nil
}

// scanFile searches one file through the sandbox (CODE-TOOLS §2.4); buf
// is the worker's reusable read buffer (kept ≤ scratchMax).
func (e *Engine) scanFile(ctx context.Context, abs string, p *grepPlan, outside bool, scratch *[]byte) (out scanOut) {
	f, _, err := e.sb.OpenResolved(ctx, abs, outside)
	switch {
	case errors.Is(err, sandbox.ErrDeniedPath):
		return scanOut{skip: skipDenied}
	case err != nil:
		return scanOut{skip: skipUnreadable}
	case f == nil: // hard-linked (S16-7): read_file asks for approval
		return scanOut{skip: skipLinked}
	}
	defer closeInto(&err, f)
	raw, fi, err := readScratch(f, scratch)
	switch {
	case err != nil:
		return scanOut{skip: skipUnreadable}
	case raw == nil:
		return e.scanLarge(ctx, f, keyOf(abs, fi), p)
	}
	n := len(raw)
	enc := detectEnc(raw)
	if bin, _ := sniffBinary(raw, enc); bin {
		return scanOut{skip: skipBinary}
	}
	text := logical(raw, enc)
	out = p.collect(text, 1)
	if len(out.matches) > 0 || out.count > 0 {
		if n > fullHashMax {
			out.tag = statTag(keyOf(abs, fi))
		} else {
			out.tag = sumTag(sha256.Sum256(raw))
		}
	}
	return out
}

// readScratch reads a whole file into the worker buffer. One read covers
// most files without fstat (a short read of a regular file is EOF, so no
// second read for the EOF); raw is nil for files above inMemoryScan, which
// are streamed instead.
func readScratch(f *os.File, scratch *[]byte) (raw []byte, fi os.FileInfo, err error) {
	if cap(*scratch) < scratchMin {
		*scratch = make([]byte, scratchMin)
	}
	buf := (*scratch)[:cap(*scratch)]
	n, err := f.Read(buf)
	if n < len(buf) {
		if err == nil || errors.Is(err, io.EOF) {
			return buf[:n], nil, nil
		}
		return nil, nil, fmt.Errorf("codetools: read: %w", err)
	}
	if fi, err = f.Stat(); err != nil {
		return nil, nil, fmt.Errorf("codetools: stat: %w", err)
	}
	if fi.Size() > inMemoryScan {
		return nil, fi, nil
	}
	if need := int(fi.Size()) + 1; need > len(buf) {
		buf = append(buf[:n], make([]byte, need-n)...)
		if cap(buf) <= scratchMax {
			*scratch = buf
		}
	}
	k, err := io.ReadFull(f, buf[n:])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("codetools: read: %w", err)
	}
	return buf[:n+k], fi, nil
}

// collect scans an in-memory text and renders matches and context lines.
func (p *grepPlan) collect(text []byte, base int) scanOut {
	var out scanOut
	var lines []matchLine
	p.m.scanBuf(text, base, func(ml matchLine) bool {
		out.count++
		if p.perFile < 0 && p.a.Mode == "count" {
			return true
		}
		lines = append(lines, ml)
		return p.perFile < 0 || len(lines) < p.perFile
	})
	if p.a.Mode != "matches" {
		return out
	}
	for _, ml := range lines {
		out.matches = append(out.matches, grepMatch{n: ml.n, text: windowLine(text[ml.start:ml.end], ml.col)})
	}
	if c := p.a.Context; c > 0 && len(lines) > 0 {
		out.ctx = contextLines(text, lines, c)
	}
	return out
}

// contextLines renders up to c lines around every match (CODE-TOOLS §2.6).
func contextLines(text []byte, lines []matchLine, c int) map[int]string {
	ctx := map[int]string{}
	for _, ml := range lines {
		s := ml.start
		for k := 1; k <= c && s > 0; k++ {
			ps := lastIndexNL(text[:s-1]) + 1
			ctx[ml.n-k] = windowLine(text[ps:trimCR(text, ps, s-1)], 0)
			s = ps
		}
		e := ml.end
		for k := 1; k <= c; k++ {
			if e < len(text) && text[e] == '\r' {
				e++
			}
			if e >= len(text)-1 {
				break
			}
			ns := e + 1
			ne := indexNL(text[ns:], ns)
			ctx[ml.n+k] = windowLine(text[ns:trimCR(text, ns, ne)], 0)
			e = trimCR(text, ns, ne)
		}
	}
	return ctx
}

func lastIndexNL(b []byte) int { return bytes.LastIndexByte(b, '\n') }

func indexNL(b []byte, base int) int {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return base + i
	}
	return base + len(b)
}

// windowLine renders a line of at most grepLineChars chars; longer lines
// become a window centered on the match column with "…" on cut sides and a
// "[col N]" marker (CODE-TOOLS §2.6). Invalid UTF-8 is escaped.
func windowLine(line []byte, col int) string {
	if utf8.RuneCount(line) <= grepLineChars {
		s, _ := escapeInvalid(line)
		return string(s)
	}
	runeCol := utf8.RuneCount(line[:min(col, len(line))])
	total := utf8.RuneCount(line)
	from := max(0, min(runeCol-grepLineChars/2, total-grepLineChars))
	i := 0
	for range from {
		_, n := utf8.DecodeRune(line[i:])
		i += n
	}
	j := i
	for range grepLineChars {
		_, n := utf8.DecodeRune(line[j:])
		j += n
	}
	s, _ := escapeInvalid(line[i:j])
	var b strings.Builder
	if from > 0 {
		b.WriteString("…")
	}
	b.Write(s)
	if j < len(line) {
		b.WriteString("…")
	}
	b.WriteString(" [col " + strconv.Itoa(runeCol+1) + "]")
	return b.String()
}
