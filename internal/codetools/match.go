package codetools

import (
	"bytes"
	"fmt"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// lit is a literal that every match must contain (CODE-TOOLS §2.5).
type lit struct {
	s    string
	fold bool // ASCII case-insensitive
}

// matcher finds matching lines: literal fast path, required-literal
// prefilter, or plain RE2 (CODE-TOOLS §2.5).
type matcher struct {
	re        *regexp.Regexp // nil: pure literal
	lits      []lit          // any-of prefilter; empty: RE2 over the buffer
	multiline bool
}

// maxAltLits bounds an alternation turned into a literal set.
const maxAltLits = 8

// compileMatcher compiles pattern with the case mode (smart, sensitive,
// insensitive). Smart = insensitive unless a literal has an upper case letter.
func compileMatcher(pattern, mode string) (*matcher, error) {
	re0, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, fmt.Errorf("codetools: pattern: %w", err)
	}
	fold := mode == "insensitive" || mode != "sensitive" && !hasUpper(re0)
	flags := syntax.Perl
	if fold {
		flags |= syntax.FoldCase
	}
	if re0, err = syntax.Parse(pattern, flags); err != nil {
		return nil, fmt.Errorf("codetools: pattern: %w", err)
	}
	re0 = re0.Simplify()
	m := &matcher{multiline: strings.Contains(pattern, `\n`) || strings.Contains(pattern, "\n")}
	if s, f, ok := pureLiteral(re0); ok && !m.multiline {
		m.lits = []lit{{s, f}}
		return m, nil
	}
	prefix := ""
	if fold {
		prefix = "(?i)"
	}
	if m.re, err = regexp.Compile(prefix + "(?m)" + pattern); err != nil {
		return nil, fmt.Errorf("codetools: pattern: %w", err)
	}
	if !m.multiline {
		m.lits = required(re0)
	}
	return m, nil
}

func hasUpper(re *syntax.Regexp) bool {
	if (re.Op == syntax.OpLiteral || re.Op == syntax.OpCharClass) && slices.ContainsFunc(re.Rune, unicode.IsUpper) {
		return true
	}
	return slices.ContainsFunc(re.Sub, hasUpper)
}

// pureLiteral reports a pattern that is one literal (ASCII when folded).
func pureLiteral(re *syntax.Regexp) (string, bool, bool) {
	if re.Op != syntax.OpLiteral {
		return "", false, false
	}
	s := string(re.Rune)
	fold := re.Flags&syntax.FoldCase != 0
	if fold && !isASCII(s) {
		return "", false, false // non-ASCII insensitive → RE2 (§2.5 butir 4)
	}
	return s, fold, s != ""
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// required extracts a literal set of which at least one occurs in every
// match: the longest literal of a concatenation, the union of an
// alternation of ≤ maxAltLits literal sets. nil = no usable literal.
func required(re *syntax.Regexp) []lit {
	op := re.Op
	if op == syntax.OpLiteral {
		if s, f, ok := pureLiteral(re); ok {
			return []lit{{s, f}}
		}
		return nil
	}
	if op == syntax.OpCapture || op == syntax.OpPlus || op == syntax.OpRepeat && re.Min >= 1 {
		return required(re.Sub[0])
	}
	if op == syntax.OpConcat {
		var best []lit
		for _, s := range re.Sub {
			if c := required(s); c != nil && minLen(c) > minLen(best) {
				best = c
			}
		}
		return best
	}
	if op != syntax.OpAlternate {
		return nil // star, quest, classes, anchors: nothing required
	}
	var all []lit
	for _, s := range re.Sub {
		c := required(s)
		if c == nil || len(all)+len(c) > maxAltLits {
			return nil
		}
		all = append(all, c...)
	}
	return all
}

func minLen(ls []lit) int {
	if ls == nil {
		return 0
	}
	n := len(ls[0].s)
	for _, l := range ls[1:] {
		n = min(n, len(l.s))
	}
	return n
}

// index finds the first occurrence of l in b (-1 when absent). Folded
// literals search the rarer case variants of their rarest byte and verify
// with bytes.EqualFold (CODE-TOOLS §2.5 butir 4).
func (l *lit) index(b []byte) int {
	if !l.fold {
		return bytes.Index(b, []byte(l.s))
	}
	pos := rarest(l.s)
	c := l.s[pos]
	lo, up := bytes.ToLower([]byte{c})[0], bytes.ToUpper([]byte{c})[0]
	for i := pos; i < len(b); {
		j := indexEither(b[i:], lo, up)
		if j < 0 {
			return -1
		}
		start := i + j - pos
		if start+len(l.s) <= len(b) && bytes.EqualFold(b[start:start+len(l.s)], []byte(l.s)) {
			return start
		}
		i += j + 1
	}
	return -1
}

func indexEither(b []byte, x, y byte) int {
	if x == y {
		return bytes.IndexByte(b, x)
	}
	i := bytes.IndexByte(b, x)
	if i < 0 {
		return bytes.IndexByte(b, y)
	}
	if j := bytes.IndexByte(b[:i], y); j >= 0 {
		return j
	}
	return i
}

// byteRank orders ASCII by approximate frequency in source code (most
// frequent first); rarest picks the literal byte ranked last.
const byteRank = " etaoinsrlcdhupmfg_ybw.,;()=\"'v{}k/x:-0>1<2*[]j#q$z!3\\5&|48679+@%?^~`\t\n"

func rarest(s string) int {
	best, bestRank := 0, -1
	for i := range len(s) {
		r := strings.IndexByte(byteRank, bytes.ToLower([]byte{s[i]})[0])
		if r < 0 {
			r = len(byteRank) // unlisted (upper case, non-ASCII): rare
		}
		if r > bestRank {
			best, bestRank = i, r
		}
	}
	return best
}

// matchLine is a matching line: byte span within the buffer and the
// column of its first match.
type matchLine struct {
	n          int // line number
	start, end int // line bytes without the EOL
	col        int // byte offset of the first match within the line
}

// scanBuf reports the matching lines of buf (complete lines; base = number
// of its first line) to fn until fn returns false.
func (m *matcher) scanBuf(buf []byte, base int, fn func(matchLine) bool) {
	if m.multiline {
		m.scanMultiline(buf, base, fn)
		return
	}
	line, pos := base, 0 // line = number of the line starting at pos
	for pos < len(buf) {
		hit, col := m.next(buf, pos)
		if hit < 0 {
			return
		}
		ls := bytes.LastIndexByte(buf[pos:hit], '\n') + 1 + pos
		line += bytes.Count(buf[pos:ls], []byte{'\n'})
		le := bytes.IndexByte(buf[hit:], '\n')
		if le < 0 {
			le = len(buf)
		} else {
			le += hit
		}
		if col >= 0 && !fn(matchLine{n: line, start: ls, end: trimCR(buf, ls, le), col: col}) {
			return
		}
		pos = le + 1
		line++
	}
}

// next finds the next candidate at or after pos and verifies it: hit is a
// byte inside the matching line, col the match column (-1 = candidate line
// without a match; the caller skips it).
func (m *matcher) next(buf []byte, pos int) (hit, col int) {
	hit = -1
	if len(m.lits) == 0 {
		// A buffer match starts no later than any single-line match; the
		// line holding its start is then verified alone (no match across
		// '\n' outside multiline mode, like rg).
		if loc := m.re.FindIndex(buf[pos:]); loc != nil {
			hit = pos + loc[0]
		}
	}
	for i := range m.lits {
		if j := m.lits[i].index(buf[pos:]); j >= 0 && (hit < 0 || pos+j < hit) {
			hit = pos + j
		}
	}
	if hit < 0 {
		return -1, -1
	}
	ls := bytes.LastIndexByte(buf[pos:hit], '\n') + 1 + pos
	if m.re == nil {
		return hit, hit - ls
	}
	le := bytes.IndexByte(buf[hit:], '\n')
	if le < 0 {
		le = len(buf)
	} else {
		le += hit
	}
	if loc := m.re.FindIndex(buf[ls:trimCR(buf, ls, le)]); loc != nil {
		return hit, loc[0]
	}
	return hit, -1
}

// scanMultiline runs RE2 over the whole buffer; every line a match touches
// is a matching line.
func (m *matcher) scanMultiline(buf []byte, base int, fn func(matchLine) bool) {
	line, pos, last := base, 0, -1
	for _, loc := range m.re.FindAllIndex(buf, -1) {
		ls := bytes.LastIndexByte(buf[:loc[0]], '\n') + 1
		col := loc[0] - ls
		end := max(loc[1], loc[0]+1)
		for ls < len(buf) && ls < end {
			line += bytes.Count(buf[pos:ls], []byte{'\n'})
			pos = ls
			le := bytes.IndexByte(buf[ls:], '\n')
			if le < 0 {
				le = len(buf)
			} else {
				le += ls
			}
			if line > last {
				if !fn(matchLine{n: line, start: ls, end: trimCR(buf, ls, le), col: col}) {
					return
				}
				last = line
			}
			ls, col = le+1, 0
		}
	}
}

func trimCR(buf []byte, s, e int) int {
	if e > s && buf[e-1] == '\r' {
		return e - 1
	}
	return e
}
