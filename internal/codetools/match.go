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
	seq       *preSeq        // required class sequence when no literal is better
	multiline bool
	// preTrust: a pre match is already the line's match (pre is the exact
	// regex and the pattern cannot match '\n'), so the line is not run
	// through RE2 a second time.
	preTrust bool
}

// maxAltLits bounds an alternation turned into a literal set.
const maxAltLits = 8

// compileMatcher compiles pattern with the case mode (smart, sensitive,
// insensitive). Smart = insensitive unless a literal has an upper case letter.
func compileMatcher(pattern, mode string) (*matcher, error) {
	re0, fold, err := matcherSyntax(pattern, mode)
	if err != nil {
		return nil, err
	}
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
	if err := m.selectPrefilter(pattern, re0, fold); err != nil {
		return nil, err
	}
	return m, nil
}

// matcherSyntax parses pattern with the smart/sensitive/insensitive mode.
func matcherSyntax(pattern, mode string) (*syntax.Regexp, bool, error) {
	re0, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, false, fmt.Errorf("codetools: pattern: %w", err)
	}
	fold := mode == "insensitive" || mode != "sensitive" && !hasUpper(re0)
	flags := syntax.Perl
	if fold {
		flags |= syntax.FoldCase
	}
	if re0, err = syntax.Parse(pattern, flags); err != nil {
		return nil, false, fmt.Errorf("codetools: pattern: %w", err)
	}
	return re0.Simplify(), fold, nil
}

// selectPrefilter chooses the required literal set, the required class run
// or the plain RE2 candidate search.
func (m *matcher) selectPrefilter(pattern string, re0 *syntax.Regexp, fold bool) error {
	if !m.multiline {
		m.lits = required(re0)
		if seq, ok := requiredSeq(re0); ok && len(seq) >= 2 && !canMatchNL(re0) &&
			(m.lits == nil || len(seq) > minLen(m.lits)) {
			m.seq = newPreSeq(seq)
		}
	}
	if m.pre = m.re; len(m.lits) == 0 && m.seq == nil && !m.multiline {
		flags := syntax.Perl
		if fold {
			flags |= syntax.FoldCase
		}
		var err error
		m.pre, err = crlfPrefilter(pattern, flags, m.re)
		if err != nil {
			return err
		}
		m.preTrust = m.pre == m.re && !canMatchNL(re0)
	}
	return nil
}

// canMatchNL reports whether re can consume a newline (a character class
// such as [^x] does, unlike '.' without (?s)); a match of such a pattern
// may start on one line and end on the next, so a buffer match alone does
// not prove a line match.
func canMatchNL(re *syntax.Regexp) bool {
	if re.Op == syntax.OpAnyChar {
		return true
	}
	if re.Op == syntax.OpLiteral || re.Op == syntax.OpCharClass {
		return slices.Contains(re.Rune, '\n')
	}
	return slices.ContainsFunc(re.Sub, canMatchNL)
}

// maxSeqAtoms bounds the required atom sequence used as a prefilter.
const maxSeqAtoms = 16

// seqAtom matches one byte of a required sequence: a literal byte or an
// ASCII byte class (a bytewise superset for multi-byte runes is sound: an
// occurrence may only add candidates, never hide a match).
type seqAtom struct {
	cls *[256]bool // nil: literal byte
	ch  byte
	n   int // class cardinality (anchor choice)
}

func (a *seqAtom) match(c byte) bool {
	if a.cls != nil {
		return a.cls[c]
	}
	return a.ch == c
}

// asciiByte narrows an ASCII rune to a byte. Every caller first rejects
// r >= utf8.RuneSelf; the bounds check keeps the narrowing safe (G115).
func asciiByte(r rune) byte {
	if r < 0 || r >= utf8.RuneSelf {
		return 0
	}
	return byte(r)
}

// atomLiteral compiles one literal rune; non-ASCII literals stop a
// sequence (their UTF-8 bytes are not aligned with pattern positions).
func atomLiteral(r rune, fold bool) (seqAtom, bool) {
	if r >= utf8.RuneSelf {
		return seqAtom{}, false
	}
	if !fold {
		return seqAtom{ch: asciiByte(r), n: 1}, true
	}
	cls := &[256]bool{}
	n := 1
	cls[asciiByte(r)] = true
	if r >= 'a' && r <= 'z' {
		cls[asciiByte(r-'a'+'A')] = true
		n = 2
	} else if r >= 'A' && r <= 'Z' {
		cls[asciiByte(r-'A'+'a')] = true
		n = 2
	}
	return seqAtom{cls: cls, n: n}, true
}

// atomClass compiles an ASCII character class; any non-ASCII range stops
// the sequence (a bytewise check could otherwise miss a match).
func atomClass(re *syntax.Regexp) (seqAtom, bool) {
	cls := &[256]bool{}
	n := 0
	for i := 0; i+1 < len(re.Rune); i += 2 {
		lo, hi := re.Rune[i], re.Rune[i+1]
		if hi >= utf8.RuneSelf {
			return seqAtom{}, false
		}
		for c := lo; c <= hi; c++ {
			cls[asciiByte(c)] = true
			n++
		}
	}
	if n == 0 {
		return seqAtom{}, false
	}
	return seqAtom{cls: cls, n: n}, true
}

// seqPart is one element of a pattern run: the atoms it contributes at
// fixed offsets, and whether it can consume more bytes than len(atoms)
// (a variable-length repeat shifts everything after it).
type seqPart struct {
	atoms  []seqAtom
	varLen bool
	ok     bool
}

// requiredSeq is the longest run of bytes every match of re must contain
// (nil, false: none). It is the class-aware counterpart of required: a
// pattern such as \d{5} or \w+_\w+\( has no useful literal, but every
// match still contains five digits or a word byte, '_', a word byte.
//
// A run is sound only while its atoms sit at fixed offsets: atoms after a
// variable-length element are shifted by however many extra bytes it
// consumed. The builder therefore ends a run at a variable element and
// restarts it at that element's last byte, which is adjacent to the next
// pattern element (the last byte of X+ is always followed by what follows
// the repeat).
func requiredSeq(re *syntax.Regexp) ([]seqAtom, bool) {
	parts := seqParts(re, nil)
	var best, run []seqAtom
	for _, p := range parts {
		if !p.ok {
			run = nil // variable gap: restart at the next fixed element
			continue
		}
		if len(p.atoms) == 0 {
			continue
		}
		run = append(run, p.atoms...)
		if len(run) > maxSeqAtoms {
			run = run[:maxSeqAtoms]
		}
		if len(run) > len(best) {
			best = append([]seqAtom(nil), run...)
		}
		if p.varLen {
			run = []seqAtom{p.atoms[len(p.atoms)-1]} // continue from the last byte
		}
	}
	return best, best != nil
}

// seqParts flattens re into run elements: fixed-width atoms, variable
// repeats and break markers (ok=false) for elements that may consume a
// variable number of bytes or none at all.
func seqParts(re *syntax.Regexp, out []seqPart) []seqPart {
	//nolint:exhaustive // default: every other op breaks the required run
	switch re.Op {
	case syntax.OpConcat:
		for _, s := range re.Sub {
			out = seqParts(s, out)
		}
		return out
	case syntax.OpLiteral:
		fold := re.Flags&syntax.FoldCase != 0
		atoms := make([]seqAtom, 0, len(re.Rune))
		for _, r := range re.Rune {
			a, ok := atomLiteral(r, fold)
			if !ok {
				return append(out, seqPart{})
			}
			atoms = append(atoms, a)
		}
		return append(out, seqPart{atoms: atoms, ok: true})
	case syntax.OpCharClass:
		a, ok := atomClass(re)
		if !ok {
			return append(out, seqPart{})
		}
		return append(out, seqPart{atoms: []seqAtom{a}, ok: true})
	case syntax.OpAnyCharNotNL:
		cls := &[256]bool{}
		for c := range 256 {
			cls[c] = byte(c) != '\n'
		}
		return append(out, seqPart{atoms: []seqAtom{{cls: cls, n: 255}}, ok: true})
	case syntax.OpCapture:
		return seqParts(re.Sub[0], out)
	case syntax.OpPlus:
		return appendRepeat(out, seqParts(re.Sub[0], nil), 1, true)
	case syntax.OpRepeat:
		if re.Min < 1 {
			return append(out, seqPart{})
		}
		return appendRepeat(out, seqParts(re.Sub[0], nil), re.Min, re.Max != re.Min)
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine,
		syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return out // zero-width: contributes nothing
	default:
		return append(out, seqPart{}) // *, ?, alternation, unsupported: break
	}
}

// appendRepeat appends a repeat's required run: minRep copies of a fully
// fixed sub, or just the sub's first fixed element when the sub itself
// contains a variable part.
func appendRepeat(out, sub []seqPart, minRep int, variable bool) []seqPart {
	fixed := len(sub) > 0
	for _, p := range sub {
		if !p.ok || p.varLen || len(p.atoms) == 0 {
			fixed = false
			break
		}
	}
	if !fixed {
		if len(sub) == 0 || !sub[0].ok || len(sub[0].atoms) == 0 {
			return append(out, seqPart{})
		}
		sub = sub[:1]
	}
	atoms := make([]seqAtom, 0, maxSeqAtoms)
	for _, p := range sub {
		atoms = append(atoms, p.atoms...)
	}
	n := min(minRep, maxSeqAtoms)
	rep := make([]seqAtom, 0, min(len(atoms)*n, maxSeqAtoms))
	for range n {
		rep = append(rep, atoms...)
		if len(rep) >= maxSeqAtoms {
			break
		}
	}
	return append(out, seqPart{atoms: rep[:min(len(rep), maxSeqAtoms)], varLen: variable, ok: true})
}

// preSeq is a required atom sequence with its rarest atom as the anchor:
// only anchor occurrences are checked, so a scan costs one byte search
// plus one window check per occurrence.
type preSeq struct {
	atoms  []seqAtom
	anchor int
	acls   *[256]bool
	ach    byte
}

// newPreSeq picks the most selective atom as anchor: a literal by byte
// rarity, otherwise the class with the fewest members.
func newPreSeq(atoms []seqAtom) *preSeq {
	best, at := -1, 0
	for i := range atoms {
		score := 1000 + rarestRank(atoms[i].ch)
		if atoms[i].cls != nil {
			score = 100 - atoms[i].n
		}
		if score > best {
			best, at = score, i
		}
	}
	return &preSeq{atoms: atoms, anchor: at, acls: atoms[at].cls, ach: atoms[at].ch}
}

// rarestRank is the byteRank position of c (higher is rarer).
func rarestRank(c byte) int {
	if i := strings.IndexByte(byteRank, c); i >= 0 {
		return i
	}
	return len(byteRank)
}

// index returns the first start where the whole sequence occurs, or -1.
func (s *preSeq) index(b []byte) int {
	n := len(s.atoms)
	if len(b) < n {
		return -1
	}
	last := len(b) - n
	for i := 0; i <= last; {
		occ := s.findAnchor(b, i+s.anchor)
		if occ < 0 {
			return -1
		}
		start := occ - s.anchor
		if start > last {
			return -1
		}
		if s.matchAt(b, start) {
			return start
		}
		i = start + 1
	}
	return -1
}

// findAnchor is the first anchor match at or after pos.
func (s *preSeq) findAnchor(b []byte, pos int) int {
	if pos >= len(b) {
		return -1
	}
	if s.acls == nil {
		if j := bytes.IndexByte(b[pos:], s.ach); j >= 0 {
			return pos + j
		}
		return -1
	}
	for i := pos; i < len(b); i++ {
		if s.acls[b[i]] {
			return i
		}
	}
	return -1
}

// matchAt reports whether every atom matches at start.
func (s *preSeq) matchAt(b []byte, start int) bool {
	for k := range s.atoms {
		if !s.atoms[k].match(b[start+k]) {
			return false
		}
	}
	return true
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
// searched again from every candidate of a frequent one.
func (m *matcher) next(buf []byte, pos int, at *[maxAltLits]int) (hit, col int) {
	hit = m.candidate(buf, pos, at)
	if hit < 0 || hit == len(buf) && hit > 0 && buf[hit-1] == '\n' { // no line after the final EOL
		return -1, -1
	}
	ls := bytes.LastIndexByte(buf[pos:hit], '\n') + 1 + pos
	if m.re == nil || m.preTrust {
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

// candidate is the earliest candidate byte at or after pos (-1 = none).
func (m *matcher) candidate(buf []byte, pos int, at *[maxAltLits]int) int {
	if m.seq != nil {
		if j := m.seq.index(buf[pos:]); j >= 0 {
			return pos + j
		}
		return -1
	}
	if len(m.lits) == 0 {
		// A buffer match starts no later than any single-line match; the
		// line holding its start is then verified alone (no match across
		// '\n' outside multiline mode, like rg).
		if loc := m.pre.FindIndex(buf[pos:]); loc != nil {
			return pos + loc[0]
		}
		return -1
	}
	return m.earliestLit(buf, pos, at)
}

// earliestLit is the earliest literal occurrence at or after pos, reusing
// the cached positions in at.
func (m *matcher) earliestLit(buf []byte, pos int, at *[maxAltLits]int) int {
	hit := -1
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
	return hit
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
