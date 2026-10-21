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

// escapeUntrustedClose neutralises every closing boundary tag in any ASCII
// capitalisation, in the same form internal/tools uses (REVIEW-F1-SEC7
// TL7-2), so wrapped data cannot end its <untrusted> wrapper early. Matching
// is byte-wise on s itself: strings.ToLower may change byte offsets.
func escapeUntrustedClose(s string) string {
	const closeTag = "</untrusted"
	var b strings.Builder
	last := 0
	for i := strings.IndexByte(s, '<'); i >= 0 && i+len(closeTag) <= len(s); {
		if strings.EqualFold(s[i:i+len(closeTag)], closeTag) {
			b.WriteString(s[last:i])
			b.WriteString(`<\\/untrusted`)
			last = i + len(closeTag)
		}
		next := strings.IndexByte(s[i+1:], '<')
		if next < 0 {
			break
		}
		i += 1 + next
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
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
