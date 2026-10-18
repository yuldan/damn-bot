package codetools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// grepOut assembles grep output in walk order (CODE-TOOLS §2.6, §9.1).
type grepOut struct {
	p      *grepPlan
	e      *Engine
	b      []byte
	budget int // remaining page chars
	items  int // items seen (offset included)
	shown  int // items rendered
	files  int // files rendered
	more   bool
	next   int
	tags   map[string]string
	skips  [4]int
}

// add consumes one file result; false stops the walk.
func (g *grepOut) add(rel string, s scanOut) bool {
	if s.skip != skipNone {
		g.skips[s.skip]++
		return true
	}
	if s.count == 0 {
		return true
	}
	start := len(g.b)
	shownBefore := g.shown
	switch g.p.a.Mode {
	case "files":
		g.item(func() { g.b = fmt.Appendf(g.b, "%s #%s\n", rel, s.tag) })
	case "count":
		g.item(func() { g.b = fmt.Appendf(g.b, "%s #%s %d\n", rel, s.tag, s.count) })
	default:
		g.addMatches(rel, &s)
	}
	if g.shown > shownBefore {
		g.files++
		if g.tags == nil {
			g.tags = map[string]string{}
		}
		g.tags[rel] = s.tag
	} else {
		g.b = g.b[:start]
	}
	return !g.more
}

// item renders one item unless it is skipped by offset or ends the page.
func (g *grepOut) item(render func()) bool {
	g.items++
	switch {
	case g.items <= g.p.a.Offset:
		return false
	case g.more:
		return false
	case g.p.a.Limit >= 0 && g.shown >= g.p.a.Limit:
		g.more, g.next = true, g.items-1
		return false
	}
	before := len(g.b)
	render()
	if cost := len(g.b) - before; cost > g.budget && g.shown > 0 {
		g.b = g.b[:before]
		g.more, g.next = true, g.items-1
		return false
	} else {
		g.budget -= cost
	}
	g.shown++
	return true
}

// addMatches renders a file block: "path #tag", matches as "N:text",
// context as "N-text", "--" between non-adjacent blocks.
func (g *grepOut) addMatches(rel string, s *scanOut) {
	b := &block{g: g, s: s, rel: rel, c: g.p.a.Context}
	for _, m := range s.matches {
		if !g.item(func() { b.match(m) }) && g.more {
			break
		}
	}
	if b.header {
		b.context(b.last+1, b.after)
	}
}

// block renders one file's matches with merged context.
type block struct {
	g           *grepOut
	s           *scanOut
	rel         string
	c           int
	last, after int // last printed line; context owed up to after
	header      bool
}

func (b *block) context(from, to int) {
	for n := from; n <= to; n++ {
		if t, ok := b.s.ctx[n]; ok {
			b.g.b = fmt.Appendf(b.g.b, "%d-%s\n", n, t)
			b.last = n
		}
	}
}

func (b *block) match(m grepMatch) {
	if !b.header {
		b.g.b = fmt.Appendf(b.g.b, "%s #%s\n", b.rel, b.s.tag)
		b.header = true
	}
	b.context(b.last+1, min(b.after, m.n-1))
	from := max(b.last+1, m.n-b.c)
	if b.last > 0 && from > b.last+1 {
		b.g.b = append(b.g.b, "--\n"...)
	}
	b.context(from, m.n-1)
	b.g.b = fmt.Appendf(b.g.b, "%d:%s\n", m.n, m.text)
	b.last, b.after = m.n, m.n+b.c
}

func (g *grepOut) result(st *walkStats, ctxErr error, r *searchRoot) Result {
	var b strings.Builder
	unit := "matches"
	if g.p.a.Mode != "matches" {
		unit = "items"
	}
	fmt.Fprintf(&b, "grep %s · %d %s · %d files\n", strconv.Quote(g.p.a.Pattern), g.shown, unit, g.files)
	b.Write(g.b)
	if n := g.skips[skipBinary]; n > 0 {
		fmt.Fprintf(&b, "skipped: %d binary\n", n)
	}
	unread := g.skips[skipUnreadable]
	denied := g.skips[skipDenied]
	var paths []string
	if st != nil {
		denied += st.denied
		unread += st.nUnread
		paths = st.unreadable
	}
	if unread > 0 {
		fmt.Fprintf(&b, "skipped: %d unreadable %s\n", unread, strings.Join(paths, " "))
	}
	if denied > 0 {
		fmt.Fprintf(&b, "skipped: %d denied\n", denied)
	}
	meta := map[string]any{"matches": g.shown, "files": g.files, "tags": g.tags}
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		b.WriteString("truncated: timeout\n")
		meta["truncated"] = "timeout"
	}
	if g.more {
		fmt.Fprintf(&b, "next: offset=%d\n", g.next)
		meta["next"] = g.next
	}
	return Result{Content: g.e.wrapOutside(&target{outside: r.outside}, strings.TrimSuffix(b.String(), "\n")), Meta: meta}
}

// scanLarge streams a file above inMemoryScan in scanChunk steps with the
// unfinished last line carried over (CODE-TOOLS §2.4); context lines are
// collected in a second streaming pass.
func (e *Engine) scanLarge(ctx context.Context, f *os.File, key fileKey, p *grepPlan) scanOut {
	head := make([]byte, sniffLen)
	n, err := f.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return scanOut{skip: skipUnreadable}
	}
	enc := detectEnc(head[:n])
	if bin, _ := sniffBinary(head[:n], enc); bin || enc.enc != EncUTF8 {
		return scanOut{skip: skipBinary} // UTF-16 above 16 MiB is not transcoded (ADR-103)
	}
	var out scanOut
	buf := make([]byte, 0, 2*scanChunk)
	line, pos := 1, int64(0)
	for done := false; !done && ctx.Err() == nil; {
		chunk := buf[len(buf):cap(buf)][:scanChunk]
		k, rerr := f.ReadAt(chunk, pos)
		pos += int64(k)
		buf = buf[:len(buf)+k]
		done = rerr != nil || k == 0
		cut := len(buf)
		if !done {
			cut = lastIndexNL(buf) + 1
			if cut == 0 {
				cut = len(buf) // a line longer than the chunk: scan as is
			}
		}
		part := p.collect(buf[:cut], line)
		out.count += part.count
		out.matches = append(out.matches, part.matches...)
		line += strings.Count(string(buf[:cut]), "\n")
		buf = append(buf[:0], buf[cut:]...)
		if p.perFile > 0 && len(out.matches) >= p.perFile {
			out.matches = out.matches[:p.perFile]
			break
		}
	}
	if out.count == 0 {
		return out
	}
	out.tag = statTag(key)
	if c := p.a.Context; c > 0 && len(out.matches) > 0 {
		out.ctx = e.contextPass(f, out.matches, c)
	}
	return out
}

// contextPass reads lines again and keeps those near a match.
func (e *Engine) contextPass(f *os.File, ms []grepMatch, c int) map[int]string {
	want := map[int]bool{}
	for _, m := range ms {
		for n := m.n - c; n <= m.n+c; n++ {
			want[n] = true
		}
	}
	ctx := map[int]string{}
	br := bufio.NewReaderSize(io.NewSectionReader(f, 0, 1<<62), tailChunk)
	maxN := ms[len(ms)-1].n + c
	for n := 1; n <= maxN; n++ {
		l, _, err := readLine(br)
		if want[n] {
			ctx[n] = windowLine(trimEOL(l), 0)
		}
		if err != nil {
			break
		}
	}
	return ctx
}
