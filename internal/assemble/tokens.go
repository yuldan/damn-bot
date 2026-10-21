package assemble

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Counter counts prompt tokens. The production implementation is the COST-09
// estimator in internal/budget (budget.Count(s).Raw); tests may inject an
// equivalent counter. The counter must be deterministic and monotonic.
type Counter interface {
	Count(string) int
}

// CounterFunc adapts a function to [Counter].
type CounterFunc func(string) int

// Count calls f.
func (f CounterFunc) Count(s string) int { return f(s) }

// truncateTokens returns the longest prefix of s that counts at most max.
// Counting is delegated entirely to c, so the result respects the same
// per-script formula the rest of the turn uses.
func truncateTokens(s string, max int, c Counter) string {
	if max <= 0 || s == "" {
		return ""
	}
	if c.Count(s) <= max {
		return s
	}
	runes := []rune(s)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if c.Count(string(runes[:mid])) <= max {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return string(runes[:lo])
}

// clip truncates s to max tokens, reserving room for marker. marker is
// appended only when s itself does not fit.
func clip(s string, max int, marker string, c Counter) string {
	if s == "" || max <= 0 {
		return ""
	}
	if c.Count(s) <= max {
		return s
	}
	room := max - c.Count(marker)
	if room <= 0 {
		return truncateTokens(marker, max, c)
	}
	return truncateTokens(s, room, c) + marker
}

// clipWrapped returns head+body+tail when it fits max, otherwise head + the
// longest body prefix + marker + tail that fits. head and tail (an
// <untrusted> wrapper) are never cut, so a clipped wrapper always closes; ""
// when not even the empty body fits.
func clipWrapped(head, body, tail, marker string, max int, c Counter) string {
	if c.Count(head+body+tail) <= max {
		return head + body + tail
	}
	runes := []rune(body)
	fits := func(n int) bool { return c.Count(head+string(runes[:n])+marker+tail) <= max }
	if !fits(0) {
		return ""
	}
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if fits(mid) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return head + string(runes[:lo]) + marker + tail
}

// clipWrappedTier clips a text tier whose <untrusted> wrappers passed
// [validWrappers] to max tokens without splitting a tag: the cut moves before
// a tag it would split, and a wrapper the cut leaves open is closed after
// marker, so the result is still well formed (REVIEW-F2-SEC4 ASM4-3).
func clipWrappedTier(s string, max int, marker string, c Counter) string {
	const closer = "\n</untrusted>"
	if s == "" || c.Count(s) <= max {
		return s
	}
	cut := truncateTokens(s, max-c.Count(marker+closer), c)
	if i := strings.LastIndexByte(cut, '<'); i >= 0 && !strings.Contains(cut[i:], ">") {
		cut = cut[:i]
	}
	// Live tags are exact and lowercase after validation; escaped data
	// (`<\\untrusted`) cannot match either needle.
	if strings.LastIndex(cut, "<untrusted") > strings.LastIndex(cut, "</untrusted") {
		return cut + marker + closer
	}
	return cut + marker
}

// untrustedName is the boundary tag name; untrustedTagAt reports whether s[i:]
// starts an opening ("<untrusted") or closing ("</untrusted") boundary in any
// ASCII capitalisation, whatever follows, and returns the matched length.
const untrustedName = "untrusted"

func untrustedTagAt(s string, i int) (closing bool, n int, ok bool) {
	if s[i] != '<' {
		return false, 0, false
	}
	name := strings.TrimPrefix(s[i+1:], "/")
	if len(name) < len(untrustedName) || !strings.EqualFold(name[:len(untrustedName)], untrustedName) {
		return false, 0, false
	}
	n = len(s) - i - len(name) + len(untrustedName) // "<" or "</", then the name
	return n > len("<"+untrustedName), n, true
}

// escapeUntrusted neutralises every <untrusted boundary tag, closing or
// opening, in any ASCII capitalisation and whatever follows it (attributes,
// whitespace, "/>", a partial tag) by inserting `\\` after '<':
// "</untrusted" becomes `<\\/untrusted` as in internal/tools (REVIEW-F1-SEC7
// TL7-2) and "<untrusted" becomes `<\\untrusted` (REVIEW-F2-SEC4 ASM4-2).
// Escaped data holds no live tag, so it can neither end its wrapper early nor
// open a nested one; the function is idempotent. Matching is byte-wise on s
// itself: strings.ToLower may change byte offsets.
func escapeUntrusted(s string) string {
	var b strings.Builder
	last := 0
	for i := range len(s) {
		if _, n, ok := untrustedTagAt(s, i); ok {
			b.WriteString(s[last:i] + `<\\` + s[i+1:i+n])
			last = i + n // the tag name holds no '<', so the scan goes on from i
		}
	}
	if last == 0 {
		return s
	}
	return b.String() + s[last:]
}

// truncateRunes shortens s to at most n runes without splitting a rune.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func itoa(n int) string { return strconv.Itoa(n) }

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
