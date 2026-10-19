package netx

import (
	"bytes"
	"encoding/binary"
	"math"
)

// patternRunsHit scans the whitespace-delimited runs holding a decoded
// non-space byte (NETX-26): every pattern's unbounded part is non-space,
// and the two with spaces in their bounded head (authorization, PEM) make
// the whole layer scanned (TestDLPPatternDifferential).
func (d *dlp) patternRunsHit(layer []byte, tr *spanTrack) bool {
	if tr.patFull || containsAuthorization(layer) || bytes.Contains(layer, []byte("-----BEGIN")) {
		return d.patternHit(layer)
	}
	// One call over the runs joined by '\n' (no match crosses whitespace).
	buf, ok := d.scratch.Get().(*[]byte)
	if !ok {
		buf = new([]byte)
	}
	joined, end := (*buf)[:0], 0
	for _, pi := range tr.pats {
		p := int(pi)
		if p < end {
			continue // inside the previous run
		}
		lo := p
		for lo > end && !isSpace(layer[lo-1]) {
			lo--
		}
		hi := p + 1
		for hi < len(layer) && !isSpace(layer[hi]) {
			hi++
		}
		joined = append(append(joined, layer[lo:hi]...), '\n')
		end = hi
	}
	hit := len(joined) > 0 && d.patternHit(joined)
	*buf = joined
	d.scratch.Put(buf)
	return hit
}

// isSpace is RE2 \s (redact's whitespace).
func isSpace(c byte) bool { return c == ' ' || c >= '\t' && c <= '\r' }

// containsAuthorization reports whether b holds "authorization" in any
// ASCII case. It pivots on the rare 'z' (SIMD IndexByte per case) instead
// of testing every 'a'.
func containsAuthorization(b []byte) bool {
	const needle, pivot = "authorization", 7 // needle[pivot] == 'z'
	for _, z := range [...]byte{'z', 'Z'} {
		for i := 0; ; {
			j := bytes.IndexByte(b[i:], z)
			if j < 0 {
				break
			}
			at := i + j - pivot
			if at >= 0 && at+len(needle) <= len(b) && bytes.EqualFold(b[at:at+len(needle)], []byte(needle)) {
				return true
			}
			i += j + 1
		}
	}
	return false
}

// plusSpans adds a secret window at each '+' of run (written at off): a
// space can be part of a secret form.
func plusSpans(tr *spanTrack, off int, run []byte) {
	for k, c := range run {
		if c == '+' {
			tr.addSpan(off + k)
		}
	}
}

// copyFormSpaces copies src to dst with '+' → ' ', eight bytes at a time
// (SWAR; form bodies have a '+' every few bytes). It returns len(src).
func copyFormSpaces(dst, src []byte) int {
	const (
		ones = 0x0101010101010101
		high = 0x8080808080808080
		low7 = 0x7f7f7f7f7f7f7f7f
		plus = '+' * ones
		toSp = ('+' ^ ' ') * ones // XOR turning '+' into ' '
	)
	n := len(src)
	k := 0
	for ; k+8 <= n; k += 8 {
		w := binary.LittleEndian.Uint64(src[k:])
		t := w ^ plus // zero bytes where w has '+'
		m := ^((t&low7 + low7) | t) & high
		binary.LittleEndian.PutUint64(dst[k:], w^((m>>7)*0xff&toSp))
	}
	for ; k < n; k++ {
		c := src[k]
		if c == '+' {
			c = ' '
		}
		dst[k] = c
	}
	return n
}

// spanTrack merges decoded positions closer than gap into [first, last]
// spans and lists decoded non-space positions for the pattern runs (up to
// patLimit; patFull: whole-layer scan).
type spanTrack struct {
	first, last, gap int // current span; last < 0 = none
	spans            [][2]int
	pats             []int32
	patLimit         int
	patFull          bool
	decoded          bool // some byte of the layer was decoded
}

func (t *spanTrack) add(p int, c byte, secret bool) {
	t.decoded = true
	if !isSpace(c) {
		t.addPat(p)
	}
	if secret {
		t.addSpan(p)
	}
}

// addSpan extends the current span to p or starts a new one (inlined in
// the decode loops: '+' decodes to a space every few bytes of form data).
func (t *spanTrack) addSpan(p int) {
	if t.last >= 0 && p-t.last <= t.gap {
		t.last = p
		return
	}
	t.flush()
	t.first, t.last = p, p
}

func (t *spanTrack) addPat(p int) {
	switch {
	case t.patFull:
	case len(t.pats) >= t.patLimit || p < 0 || p > math.MaxInt32:
		t.patFull = true
	default:
		t.pats = append(t.pats, int32(p))
	}
}

func (t *spanTrack) flush() {
	if t.last >= 0 {
		t.spans = append(t.spans, [2]int{t.first, t.last})
	}
}

func (t *spanTrack) done() [][2]int {
	t.flush()
	return t.spans
}

// widen turns [first, last] decoded spans into the ranges the secret
// matcher scans: d.region folded bytes (NETX-26) on each side, merged; each
// walk starts where the previous range ended, so the work is O(len(layer)).
func (d *dlp) widen(layer []byte, spans [][2]int) [][2]int {
	out := spans[:0]
	end := 0
	for _, sp := range spans {
		lo := d.walkBack(layer, sp[0], end)
		hi := d.walkFwd(layer, max(sp[1]+1, end))
		if len(out) > 0 && lo <= out[len(out)-1][1] {
			out[len(out)-1][1] = hi
		} else {
			out = append(out, [2]int{lo, hi})
		}
		end = hi
	}
	return out
}

// walkBack returns the start of the d.region folded bytes before p, not
// below floor.
func (d *dlp) walkBack(layer []byte, p, floor int) int {
	for n := 0; p > floor && n < d.region; {
		p--
		if !d.folds(layer[p]) {
			n++
		}
	}
	return p
}

// walkFwd returns the end of the d.region folded bytes from p.
func (d *dlp) walkFwd(layer []byte, p int) int {
	for n := 0; p < len(layer) && n < d.region; p++ {
		if !d.folds(layer[p]) {
			n++
		}
	}
	return p
}

// secretByte reports whether decoded byte c can be part of a new secret
// occurrence: it is kept by fold and occurs in some folded form, or fold
// drops it (CR/LF), joining its neighbours. Other decoded bytes split any
// occurrence, so no window is needed around them (NETX-26/31).
func (d *dlp) secretByte(c byte) bool { return d.folds(c) || d.inForm[d.lower[c]] }

// folds reports whether fold drops c.
func (d *dlp) folds(c byte) bool { return !d.keepCRLF && (c == '\r' || c == '\n') }
