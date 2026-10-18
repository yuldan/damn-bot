package redact

import "bytes"

// mode tells a matcher what the end of the data means.
type mode uint8

const (
	modeStream mode = iota // more input may follow: an open construct needs more
	modeFinal              // end of input terminates every open construct
	modeForced             // Stream hold cap reached: cut open tails fail-closed
)

type status uint8

const (
	stNone status = iota // definitely no match here, whatever follows
	stOK                 // matched (step: continue)
	stMore               // undecided until more input arrives
	stCut                // forced cut of an unbounded tail
)

// match is a replacement of data[start:end] by [REDACTED:p.typ].
type match struct {
	p          *pattern
	start, end int
	cut        bool // the token continues past end; Stream drops the rest
}

func upper(c byte) byte {
	if 'a' <= c && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}

func lower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c - 'A' + 'a'
	}
	return c
}

// hasPrefix compares b with the first len(b) bytes of s; s is lower case
// when fold is set.
func hasPrefix(b []byte, s string, fold bool) bool {
	for k, c := range b {
		if fold {
			c = lower(c)
		}
		if c != s[k] {
			return false
		}
	}
	return true
}

func matchLit(data []byte, j int, s string, fold bool, md mode) (int, status) {
	rest := data[j:]
	if len(rest) >= len(s) {
		if hasPrefix(rest[:len(s)], s, fold) {
			return j + len(s), stOK
		}
		return j, stNone
	}
	if md == modeStream && hasPrefix(rest, s, fold) {
		return j, stMore
	}
	return j, stNone
}

func matchRun(data []byte, j int, s *step, md mode) (int, status) {
	k := j
	for k < len(data) && (s.max == 0 || k-j < s.max) && s.set[data[k]] {
		k++
	}
	if k == len(data) && (s.max == 0 || k-j < s.max) {
		switch {
		case md == modeStream:
			return k, stMore
		case md == modeForced && s.max == 0:
			return k, stCut
		case md == modeForced:
			return k, stNone
		}
	}
	if k-j < s.min {
		return k, stNone
	}
	return k, stOK
}

// lastAt runs set from j and stops at the run's last '@' (at least one byte
// in): a URL password may itself hold '@' and '/' (ADR-096 butir 6, R-02).
func lastAt(data []byte, j int, set *[256]bool, md mode) (int, status) {
	at, k := -1, j
	for ; k < len(data) && set[data[k]]; k++ {
		if data[k] == '@' && k > j {
			at = k
		}
	}
	switch {
	case k == len(data) && md == modeStream:
		return k, stMore
	case k == len(data) && md == modeForced:
		return k, stCut
	case at < 0:
		return k, stNone
	}
	return at, stOK
}

func behind(data []byte, j int, s *step) status {
	if j < s.min {
		return stNone
	}
	for _, c := range data[j-s.min : j] {
		if !s.set[c] {
			return stNone
		}
	}
	return stOK
}

func keyLabel(head []byte) status {
	if bytes.HasSuffix(head, []byte("PRIVATE KEY")) || bytes.HasSuffix(head, []byte("PRIVATE KEY BLOCK")) {
		return stOK
	}
	return stNone
}

// pemEnd finds the end of the first "-----END <label>-----" line at or after
// j whose label names a private key. Without one the block runs to the end of
// the data: fail-closed (ADR-071 butir 2).
func pemEnd(data []byte, j int, label *[256]bool, md mode) (int, status) {
	const marker, dashes = "-----END ", "-----"
	for {
		k := bytes.Index(data[j:], []byte(marker))
		if k < 0 {
			break
		}
		e := j + k + len(marker)
		l := e
		for l < len(data) && l-e < 64 && label[data[l]] {
			l++
		}
		n := min(len(data)-l, len(dashes))
		if l == len(data) || (n < len(dashes) && hasPrefix(data[l:], dashes, false)) {
			break // the END line itself is still incomplete
		}
		if keyLabel(data[e:l]) == stOK && hasPrefix(data[l:l+n], dashes, false) {
			return l + n, stOK
		}
		j += k + 1
	}
	switch md {
	case modeStream:
		return len(data), stMore
	case modeForced:
		return len(data), stCut
	case modeFinal:
	}
	return len(data), stOK
}

// match runs p anchored at data[i].
func (p *pattern) match(data []byte, i int, md mode) (match, status) {
	m := match{p: p, start: i, end: -1}
	j := i
	for k := range p.steps {
		s := &p.steps[k]
		st := stOK
		switch s.op {
		case opLit:
			j, st = matchLit(data, j, s.lit, s.fold, md)
		case opRun:
			j, st = matchRun(data, j, s, md)
		case opBehind:
			st = behind(data, j, s)
		case opMark:
			m.start = j
		case opMarkEnd:
			m.end = j
		case opKeyLabel:
			st = keyLabel(data[i:j])
		case opPEMEnd:
			j, st = pemEnd(data, j, s.set, md)
		case opLastAt:
			j, st = lastAt(data, j, s.set, md)
		}
		switch st {
		case stNone, stMore:
			m.end = j // how far the attempt read; see matchAt
			return m, st
		case stCut:
			m.end, m.cut = len(data), true
			return m, stOK
		case stOK:
		}
	}
	if m.end < 0 {
		m.end = j
	}
	return m, stOK
}

// matchAt tries every pattern that can start with data[i], in priority
// order; the first decided or undecided candidate wins. skip memoises, per
// built-in pattern, the position a failed attempt read up to: a retry that
// starts inside that span re-reads the same runs and fails the same way, so
// skipping it keeps each scan linear (EC-SEC-008).
func (r *Redactor) matchAt(data []byte, i int, md mode, skip *[64]int) (match, status) {
	for _, p := range r.byFirst[data[i]] {
		head := p.first.lit
		if p.idx >= 0 && i < skip[p.idx] {
			continue
		}
		if len(head) > 1 && i+1 < len(data) && !hasPrefix(data[i+1:i+2], head[1:2], p.first.fold) {
			continue // cheap reject on the second byte, the common case in prose
		}
		m, st := p.match(data, i, md)
		if st != stNone {
			return m, st
		}
		if p.idx >= 0 && md != modeForced {
			// Only retries whose leading literal lies wholly inside the read
			// span are known to fail; one straddling its end may match.
			skip[p.idx] = m.end - (len(head) - 1)
		}
	}
	return match{}, stNone
}

// extend grows m over every known secret that starts inside it and runs past
// its end, so an adjacent token never strands the tail of a secret.
func (r *Redactor) extend(data []byte, m *match, md mode) status {
	for q := m.start; q < m.end; q++ {
		for _, p := range r.byFirst[data[q]] {
			if !p.secret {
				break // known secrets precede the built-in patterns
			}
			e, st := matchLit(data, q, p.steps[0].lit, false, md)
			if st == stMore {
				return stMore
			}
			if st == stOK && e > m.end {
				m.end = e
			}
		}
	}
	return stOK
}

// scan redacts data[from:] into dst, leftmost match first. It returns dst
// (still nil when nothing matched), last (start of the input tail not yet
// copied to dst), safe (input before it is decided; in modeStream the rest
// must be held) and the pattern of a fail-closed cut that ends the data.
func (r *Redactor) scan(dst, data []byte, from int, md mode) ([]byte, int, int, *pattern) {
	var skip [64]int
	last := from
	for i := from; i < len(data); {
		// The last byte has no successor: it still reaches matchAt so a
		// stream can hold a partial prefix.
		if r.byFirst[data[i]] == nil || i+1 < len(data) && !r.pair(data[i], data[i+1]) {
			i++
			continue
		}
		m, st := r.matchAt(data, i, md, &skip)
		if st == stOK && !m.cut {
			st = r.extend(data, &m, md)
		}
		if st == stMore && len(data)-i >= r.hold {
			m, st = r.matchAt(data, i, modeForced, &skip)
		}
		switch st {
		case stMore:
			return dst, last, i, nil
		case stNone, stCut: // matchAt reports a cut as stOK with m.cut
			i++
			continue
		case stOK:
		}
		dst = append(dst, data[last:i]...)
		if m.start > i {
			// Kept context before the replacement (URL user, header name)
			// is itself scanned, e.g. a token used as URL user name.
			dst = append(dst, data[i])
			var l int
			dst, l, _, _ = r.scan(dst, data[:m.start], i+1, modeFinal)
			dst = append(dst, data[l:m.start]...)
		}
		dst = append(append(append(dst, SentinelPrefix...), m.p.typ...), ']')
		last, i = m.end, m.end
		if m.cut {
			return dst, last, last, m.p
		}
	}
	return dst, last, len(data), nil
}
