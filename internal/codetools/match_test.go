package codetools

import (
	"slices"
	"testing"
)

// TestMatcherLineEnds pins the rg parity of line ends (CT-21): no line
// exists after the final EOL, and `$` holds before "\r\n" also for
// patterns without a required literal (lines are matched without '\r').
func TestMatcherLineEnds(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		pattern, text string
		want          []int
	}{
		{`^$`, "a\n\nb\n", []int{2}},
		{`^$`, "a\r\n\r\nb\r\n", []int{2}},
		{`^$`, "a\nb", nil},
		{`$`, "a\nb", []int{1, 2}},
		{`[{}]$`, "x {\r\ny\r\n}\r\n", []int{1, 3}},
		{`^\s+$`, "a\r\n  \r\n\r\n", []int{2}},
		{`\)$`, "f()\r\ng(\r\n", []int{1}},
		{`b$|^q`, "ab\r\nqz\r\n", []int{1, 2}},
	} {
		m, err := compileMatcher(c.pattern, "sensitive")
		if err != nil {
			t.Fatal(err)
		}
		var got []int
		m.scanBuf([]byte(c.text), 1, func(ml matchLine) bool { got = append(got, ml.n); return true })
		if !slices.Equal(got, c.want) {
			t.Errorf("%q in %q: lines %v, want %v", c.pattern, c.text, got, c.want)
		}
	}
}

// TestRequiredLiterals pins the prefilter choice of CODE-TOOLS §2.5 butir
// 3: finite runs are expanded (RE2 factors `def|func|fn` into
// `def|f(?:unc|n)`), the longest set wins, the first on a tie.
func TestRequiredLiterals(t *testing.T) {
	t.Parallel()
	for pattern, want := range map[string][]string{
		`^(def|func|fn) `:   {"def ", "func ", "fn "},
		`\w+_\w+\(`:         {"_"},
		`def \w+_prompt\(`:  {"_prompt("},
		`(lease|fence)_\d`:  {"lease_", "fence_"},
		`\d{5}`:             nil,
		`x*`:                nil,
		`(?i)ab(cd|ef)`:     {"ABCD", "ABEF"}, // folded literals keep RE2's upper case
		`(a|bb)\w+(cc|dd)e`: {"cce", "dde"},
	} {
		m, err := compileMatcher(pattern, "sensitive")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, l := range m.lits {
			got = append(got, l.s)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%q: literals %q, want %q", pattern, got, want)
		}
	}
}
