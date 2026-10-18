package exec

import (
	"bytes"
	"regexp"
	"time"
)

// Interactive prompt detection (§A.6, ADR-072): a foreground tree whose
// output has been silent for promptSilence and whose last partial line
// looks like a prompt is killed and returned as waiting_for_input.
const (
	promptSilence = 1500 * time.Millisecond
	// wchanSilence: Linux only, a tree blocked reading a terminal or pipe
	// for this long counts as waiting even without a matching line.
	wchanSilence = 5 * time.Second
	// maxPromptLine: longer partial lines are output, not prompts.
	maxPromptLine = 200
	// promptTail: bytes of the output tail examined for the last line.
	promptTail = 1024
)

// Input kinds of a detected prompt (Result.InputKind).
const (
	InputSecret  = "secret"
	InputConfirm = "confirm"
	InputPager   = "pager"
	InputText    = "text"
)

type promptRule struct {
	kind string
	re   *regexp.Regexp
	// maxLen: the rule applies only to lines up to this length (0 = any).
	maxLen int
}

// newPromptRules compiles the §A.6 table (RE2, case-insensitive), in
// precedence order.
func newPromptRules() []promptRule {
	r := func(kind, expr string, maxLen int) promptRule {
		return promptRule{kind: kind, re: regexp.MustCompile(expr), maxLen: maxLen}
	}
	return []promptRule{
		r(InputSecret, `(?i)(password|passphrase|passcode|pin|token)[^:\n]{0,40}:\s*$`, 0),
		r(InputSecret, `(?i)^\[sudo\] `, 0),
		r(InputConfirm, `(?i)\[(y/n|yes/no)\]\s*[:?]?\s*$`, 0),
		r(InputConfirm, `(?i)\((y/n|yes/no)\)\s*[:?]?\s*$`, 0),
		r(InputConfirm, `(?i)continue connecting \(yes/no`, 0),
		r(InputPager, `^\(END\)$`, 0),
		r(InputPager, `--More--`, 0),
		r(InputPager, `^:$`, 0),
		r(InputText, `(?i)press (any key|enter|return)`, 0),
		r(InputText, `(?i)username for '`, 0),
		r(InputText, `[?:]\s*$`, 120),
	}
}

// lastLine is the last partial line of an output tail: the text after the
// last newline (and after the last carriage return, which redraws the
// line), without ANSI escapes; "" when the tail ends a line or the line is
// longer than maxPromptLine.
func lastLine(tail []byte) string {
	clean := stripANSI(nil, tail)
	last := bytes.TrimRight(clean[bytes.LastIndexByte(clean, '\n')+1:], "\r")
	last = last[bytes.LastIndexByte(last, '\r')+1:]
	if len(bytes.TrimSpace(last)) == 0 || len(last) > maxPromptLine {
		return ""
	}
	return string(last)
}

// promptKind classifies a last line by the §A.6 table ("" = no prompt).
func promptKind(rules []promptRule, line string) string {
	if line == "" {
		return ""
	}
	for _, r := range rules {
		if (r.maxLen == 0 || len(line) <= r.maxLen) && r.re.MatchString(line) {
			return r.kind
		}
	}
	return ""
}

// stripANSI appends src without ANSI escape sequences (CSI `ESC [ … final`,
// OSC `ESC ] … BEL|ST`, and two-byte `ESC x`) to dst.
func stripANSI(dst, src []byte) []byte {
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c != 0x1b || i+1 >= len(src) {
			dst = append(dst, c)
			continue
		}
		i += escapeLen(src[i:]) - 1
	}
	return dst
}

// escapeLen is the length of the escape sequence at s[0] == ESC (the rest
// of s when unterminated).
func escapeLen(s []byte) int {
	switch s[1] {
	case '[': // CSI: parameters and intermediates, then a final 0x40–0x7E
		for k := 2; k < len(s); k++ {
			if s[k] >= 0x40 && s[k] <= 0x7e {
				return k + 1
			}
		}
	case ']': // OSC: until BEL or ESC '\'
		for k := 2; k < len(s); k++ {
			if s[k] == 0x07 {
				return k + 1
			}
			if s[k] == 0x1b && k+1 < len(s) && s[k+1] == '\\' {
				return k + 2
			}
		}
	default:
		return 2
	}
	return len(s)
}
