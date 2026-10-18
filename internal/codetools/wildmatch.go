package codetools

import "strings"

// wildmatch results (port of git's wildmatch.c dowild, MIT-compatible
// reimplementation from the documented algorithm).
const (
	wmMatch = iota
	wmNoMatch
	wmAbortAll
	wmAbortToStarStar
)

// wildmatch reports whether text matches the git glob p. pathname = git's
// WM_PATHNAME: '*', '?' and classes never match '/', "**" spans dirs.
func wildmatch(p, text string, pathname bool) bool {
	return dowild(p, text, pathname) == wmMatch
}

func isGlobSpecial(c byte) bool { return c == '*' || c == '?' || c == '[' || c == '\\' }

// dowild matches text against p (git dowild): star runs go to star, every
// other item to matchItem.
func dowild(p, text string, pathname bool) int {
	pi, ti := 0, 0
	for ; pi < len(p); pi, ti = pi+1, ti+1 {
		if p[pi] == '*' {
			res, npi, nti := star(p, pi, text, ti, pathname)
			if res >= 0 {
				return res
			}
			pi, ti = npi, nti
			continue
		}
		if ti >= len(text) {
			return wmAbortAll
		}
		n, res := matchItem(p[pi:], text[ti], pathname)
		if res != wmMatch {
			return res
		}
		pi += n
	}
	if ti < len(text) {
		return wmNoMatch
	}
	return wmMatch
}

// matchItem matches one non-star item at the start of p against c; n is
// the number of extra pattern bytes it used.
func matchItem(p string, c byte, pathname bool) (n, res int) {
	switch p[0] {
	case '[':
		k, ok := matchClass(p[1:], c)
		switch {
		case k < 0:
			return 0, wmAbortAll
		case !ok || pathname && c == '/':
			return 0, wmNoMatch
		}
		return k, wmMatch
	case '?':
		if pathname && c == '/' {
			return 0, wmNoMatch
		}
		return 0, wmMatch
	case '\\':
		if len(p) < 2 || p[1] != c {
			return 0, wmNoMatch
		}
		return 1, wmMatch
	}
	if p[0] != c {
		return 0, wmNoMatch
	}
	return 0, wmMatch
}

// starRun measures the run of '*' at p[pi]: last is its final index and
// matchSlash whether it may cross '/' ("**" between slashes, or any star
// without pathname).
func starRun(p string, pi int, pathname bool) (last int, matchSlash bool) {
	if pi+1 >= len(p) || p[pi+1] != '*' {
		return pi, !pathname
	}
	prev := pi - 1
	for pi+1 < len(p) && p[pi+1] == '*' {
		pi++
	}
	rest := p[pi+1:]
	return pi, (prev < 0 || p[prev] == '/') && (rest == "" || rest[0] == '/' || strings.HasPrefix(rest, `\\/`))
}

// star handles a run of '*' at p[pi]: res ≥ 0 is the final result; else
// the loop continues with p[npi] matched against text[nti].
func star(p string, pi int, text string, ti int, pathname bool) (res, npi, nti int) {
	last, matchSlash := starRun(p, pi, pathname)
	if matchSlash && last > pi && last+1 < len(p) && p[last+1] == '/' && dowild(p[last+2:], text[ti:], pathname) == wmMatch {
		return wmMatch, 0, 0 // "**/" matching zero directories
	}
	pi = last + 1
	switch {
	case pi >= len(p) && !matchSlash && strings.IndexByte(text[ti:], '/') >= 0:
		return wmNoMatch, 0, 0
	case pi >= len(p):
		return wmMatch, 0, 0
	case !matchSlash && p[pi] == '/':
		slash := strings.IndexByte(text[ti:], '/')
		if slash < 0 {
			return wmNoMatch, 0, 0
		}
		return -1, pi, ti + slash // the loop consumes the slash with p[pi]
	}
	return starScan(p[pi:], text, ti, pathname, matchSlash), 0, 0
}

// starScan tries every position where the rest of the pattern may start.
func starScan(p, text string, ti int, pathname, matchSlash bool) int {
	for ; ti < len(text); ti++ {
		if !isGlobSpecial(p[0]) {
			k, ok := skipTo(text, ti, p[0], matchSlash)
			if !ok && matchSlash {
				return wmAbortAll
			}
			if !ok {
				return wmAbortToStarStar
			}
			ti = k
		}
		m := dowild(p, text[ti:], pathname)
		if m != wmNoMatch && (!matchSlash || m != wmAbortToStarStar) {
			return m
		}
		if m == wmNoMatch && !matchSlash && text[ti] == '/' {
			return wmAbortToStarStar
		}
	}
	return wmAbortAll
}

// skipTo finds the next c at or after ti, not crossing '/' unless
// matchSlash: the bytes before the literal belong to the star.
func skipTo(text string, ti int, c byte, matchSlash bool) (int, bool) {
	for k := ti; k < len(text); k++ {
		switch {
		case text[k] == c:
			return k, true
		case !matchSlash && text[k] == '/':
			return k, false
		}
	}
	return len(text), false
}

// matchClass matches t against the bracket expression starting after '['
// in p; n is the number of pattern bytes consumed including ']' (-1 when
// malformed: unterminated or unknown [:class:]).
func matchClass(p string, t byte) (n int, ok bool) {
	i := 0
	negated := i < len(p) && (p[i] == '!' || p[i] == '^')
	if negated {
		i++
	}
	var prev byte
	matched := false
	for first := true; i < len(p) && (first || p[i] != ']'); first = false {
		var hit bool
		var used int
		hit, prev, used = classItem(p[i:], prev, t)
		if used < 0 {
			return -1, false
		}
		matched = matched || hit
		i += used
	}
	if i >= len(p) {
		return -1, false
	}
	return i + 1, matched != negated
}

// classItem consumes one bracket item at the start of p: an escaped byte, a
// range "prev-hi", a [:class:], or a byte. It returns whether t matched,
// the byte a following '-' ranges from (0 after a range or class), and the
// bytes used (-1 when malformed).
func classItem(p string, prev, t byte) (hit bool, last byte, used int) {
	c := p[0]
	switch {
	case c == '\\':
		if len(p) < 2 {
			return false, 0, -1
		}
		return t == p[1], p[1], 2
	case c == '-' && prev != 0 && len(p) > 1 && p[1] != ']':
		hi, n := p[1], 2
		if hi == '\\' {
			if len(p) < 3 {
				return false, 0, -1
			}
			hi, n = p[2], 3
		}
		return t >= prev && t <= hi, 0, n
	case c == '[' && len(p) > 1 && p[1] == ':':
		end := strings.Index(p[2:], ":]")
		if end < 0 {
			return false, 0, -1
		}
		in, known := posixClass(p[2:2+end], t)
		if !known {
			return false, 0, -1
		}
		return in, 0, end + 4
	}
	return t == c, c, 1
}

// posixClass evaluates [:name:] for a byte (ASCII classes, as git).
func posixClass(name string, t byte) (in, known bool) {
	const (
		lower = "abcdefghijklmnopqrstuvwxyz"
		upper = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		digit = "0123456789"
		punct = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"
	)
	members := map[string]string{
		"alnum": lower + upper + digit, "alpha": lower + upper, "blank": " \t", "digit": digit,
		"graph": lower + upper + digit + punct, "lower": lower, "print": " " + lower + upper + digit + punct,
		"punct": punct, "space": " \t\n\v\f\r", "upper": upper, "xdigit": digit + "abcdefABCDEF",
	}
	if name == "cntrl" {
		return t < ' ' || t == 0x7f, true
	}
	set, known := members[name]
	return known && t != 0 && strings.IndexByte(set, t) >= 0, known
}
