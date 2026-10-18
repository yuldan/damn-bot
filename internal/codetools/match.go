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
	rare int  // offset of the rarest byte, the IndexByte anchor
}

func newLit(s string, fold bool) lit { return lit{s, fold, rarest(s)} }

// matcher finds matching lines: literal fast path, required-literal
// prefilter, or plain RE2 (CODE-TOOLS §2.5).
type matcher struct {
	re        *regexp.Regexp // nil: pure literal
	pre       *regexp.Regexp // buffer-wide candidate search when lits is empty
	lits      []lit          // any-of prefilter; empty: RE2 over the buffer
	multiline bool
	// exact: a pre match free of '\r' and '\n' is the line's first match
	// (pre is re and no \A or \z tells the buffer from the line).
	exact bool
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
		m.lits = []lit{newLit(s, f)}
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
	if m.pre = m.re; len(m.lits) == 0 && !m.multiline {
		m.pre, err = crlfPrefilter(pattern, flags, m.re)
	}
	m.exact = m.pre == m.re && !strings.Contains(pattern, `\A`) && !strings.Contains(pattern, `\z`)
	return m, err
}

// crlfPrefilter is the buffer-wide RE2 of a pattern without required
// literals: `$` also matches before "\r\n" because lines are verified
// without their '\r'. It accepts a superset at no later start, so the
// per-line verification stays exact.
func crlfPrefilter(pattern string, flags syntax.Flags, re *regexp.Regexp) (*regexp.Regexp, error) {
	t, err := syntax.Parse("(?m)"+pattern, flags)
	if err != nil {
		return nil, fmt.Errorf("codetools: pattern: %w", err)
	}
	root := &syntax.Regexp{Op: syntax.OpConcat, Sub: []*syntax.Regexp{t}}
	if !crlfEnd(root) {
		return re, nil
	}
	pre, err := regexp.Compile(root.String())
	if err != nil {
		return nil, fmt.Errorf("codetools: pattern: %w", err)
	}
	return pre, nil
}

// crlfEnd rewrites every `$` below re into `\r?$`.
func crlfEnd(re *syntax.Regexp) bool {
	found := false
	for i, s := range re.Sub {
		if s.Op == syntax.OpEndLine {
			cr := &syntax.Regexp{Op: syntax.OpQuest, Sub: []*syntax.Regexp{{Op: syntax.OpLiteral, Rune: []rune{'\r'}}}}
			re.Sub[i] = &syntax.Regexp{Op: syntax.OpConcat, Sub: []*syntax.Regexp{cr, s}}
			found = true
		} else if crlfEnd(s) {
			found = true
		}
	}
	return found
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
// match: the best literal set of a concatenation (runs of finite subpatterns
// are expanded, so `^(def|fn) ` yields "def ", "fn "), the union of an
// alternation of ≤ maxAltLits literal sets. nil = no usable literal.
func required(re *syntax.Regexp) []lit {
	op := re.Op
	if op == syntax.OpLiteral {
		if s, f, ok := pureLiteral(re); ok {
			return []lit{newLit(s, f)}
		}
		return nil
	}
	if op == syntax.OpCapture || op == syntax.OpPlus || op == syntax.OpRepeat && re.Min >= 1 {
		return required(re.Sub[0])
	}
	if op == syntax.OpConcat {
		return requiredConcat(re.Sub)
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

// requiredConcat is required for a concatenation of subs: the longest
// literal set among the expanded runs of finite subpatterns and the
// required sets of the others.
func requiredConcat(subs []*syntax.Regexp) []lit {
	var best, run []lit
	pick := func(c []lit) {
		if minLen(c) > minLen(best) {
			best = c
		}
	}
	for _, s := range subs {
		e := exact(s)
		if p := product(run, e); e != nil && p != nil {
			run = p
			continue
		}
		pick(run)
		if run = e; e == nil {
			pick(required(s))
		}
	}
	pick(run)
	return best
}

// exact is the finite set of strings re matches (zero-width assertions
// match ""), nil when unbounded or above maxAltLits.
func exact(re *syntax.Regexp) []lit {
	switch re.Op {
	case syntax.OpLiteral:
		if s, f, ok := pureLiteral(re); ok {
			return []lit{newLit(s, f)}
		}
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return []lit{{}}
	case syntax.OpCapture:
		return exact(re.Sub[0])
	case syntax.OpAlternate, syntax.OpConcat:
		var set []lit
		for i, s := range re.Sub {
			e := exact(s)
			if re.Op == syntax.OpConcat && i > 0 {
				e = product(set, e)
			} else if e != nil {
				e = append(set, e...)
			}
			if set = e; e == nil || len(e) > maxAltLits {
				return nil
			}
		}
		return set
	case syntax.OpNoMatch, syntax.OpCharClass, syntax.OpAnyCharNotNL, syntax.OpAnyChar, syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat:
		return nil // unbounded or too wide for a literal set
	}
	return nil
}

// product concatenates every pair of a and b (nil when either is nil, the
// case modes differ or the set exceeds maxAltLits).
func product(a, b []lit) []lit {
	if a == nil || b == nil || len(a)*len(b) > maxAltLits {
		return nil
	}
	out := make([]lit, 0, len(a)*len(b))
	for _, x := range a {
		for _, y := range b {
			if x.s != "" && y.s != "" && x.fold != y.fold {
				return nil
			}
			out = append(out, newLit(x.s+y.s, x.fold || y.fold))
		}
	}
	return out
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

// index finds the first occurrence of l in b (-1 when absent): IndexByte
// on the literal's rarest byte (both cases when folded), then a compare
// (bytes.EqualFold when folded; CODE-TOOLS §2.5 butir 2 and 4).
func (l *lit) index(b []byte) int {
	c := l.s[l.rare]
	lo, up := c, c
	if l.fold {
		lo, up = bytes.ToLower([]byte{c})[0], bytes.ToUpper([]byte{c})[0]
	}
	for i := l.rare; i < len(b); {
		j := indexEither(b[i:], lo, up)
		if j < 0 {
			return -1
		}
		start := i + j - l.rare
		if end := start + len(l.s); end <= len(b) && (l.fold && bytes.EqualFold(b[start:end], []byte(l.s)) || !l.fold && string(b[start:end]) == l.s) {
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
	at := [maxAltLits]int{-2, -2, -2, -2, -2, -2, -2, -2}
	for pos < len(buf) {
		hit, col := m.next(buf, pos, &at)
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
// without a match; the caller skips it). at caches each literal's next
// occurrence (-1 none left, -2 unknown) so a rare literal of a set is not
// searched again from every candidate of a frequent one. A line no longer
// than grepLineChars is only tested (col 0): windowLine needs no column.
func (m *matcher) next(buf []byte, pos int, at *[maxAltLits]int) (hit, col int) {
	hit, end := m.candidate(buf, pos, at)
	if hit < 0 || hit == len(buf) && hit > 0 && buf[hit-1] == '\n' { // no line after the final EOL
		return -1, -1
	}
	ls := bytes.LastIndexByte(buf[pos:hit], '\n') + 1 + pos
	if m.re == nil || end >= 0 && m.exact && !bytes.ContainsAny(buf[hit:end], "\r\n") {
		return hit, hit - ls
	}
	le := bytes.IndexByte(buf[hit:], '\n')
	if le < 0 {
		le = len(buf)
	} else {
		le += hit
	}
	line := buf[ls:trimCR(buf, ls, le)]
	if len(line) <= grepLineChars {
		if m.re.Match(line) {
			return hit, 0
		}
		return hit, -1
	}
	if loc := m.re.FindIndex(line); loc != nil {
		return hit, loc[0]
	}
	return hit, -1
}

// candidate is the earliest candidate byte at or after pos (-1 = none);
// end >= 0 is the end of the buffer-wide match that found it.
func (m *matcher) candidate(buf []byte, pos int, at *[maxAltLits]int) (hit, end int) {
	hit, end = -1, -1
	if len(m.lits) == 0 {
		// A buffer match starts no later than any single-line match; the
		// line holding its start is then verified alone (no match across
		// '\n' outside multiline mode, like rg).
		if loc := m.pre.FindIndex(buf[pos:]); loc != nil {
			hit, end = pos+loc[0], pos+loc[1]
		}
	}
	for i := range m.lits {
		if at[i] < pos && at[i] != -1 {
			at[i] = m.lits[i].index(buf[pos:])
			if at[i] >= 0 {
				at[i] += pos
			}
		}
		if at[i] >= 0 && (hit < 0 || at[i] < hit) {
			hit = at[i]
		}
	}
	return hit, end
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
