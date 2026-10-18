package schema

import (
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// MaxDepth bounds nesting of arrays/objects in validated documents and in
// schemas. Recursion depth of the validator is therefore bounded too.
const MaxDepth = 64

// scanner is a strict RFC 8259 tokenizer over a byte slice; it never uses
// reflection and allocates only for escaped or very many member names.
// Strings must be valid UTF-8 and may not contain lone UTF-16 surrogate
// escapes.
type scanner struct {
	b    []byte
	i    int
	err  *Error   // first syntax error; non-nil stops the scan
	keys [][]byte // member names of the open objects, see keySet
}

// syntax records a syntax error at the current offset and returns false so
// callers can write `return s.syntax(...)`.
func (s *scanner) syntax(msg string) bool {
	if s.err == nil {
		s.err = &Error{Kind: KindSyntax, Offset: s.i, Msg: msg}
	}
	return false
}

// ws skips insignificant whitespace and returns the next byte (0 at EOF;
// a NUL byte also yields 0, so EOF tests use atEOF).
func (s *scanner) ws() byte {
	for s.i < len(s.b) {
		switch c := s.b[s.i]; c {
		case ' ', '\t', '\n', '\r':
			s.i++
		default:
			return c
		}
	}
	return 0
}

// atEOF skips whitespace and reports whether the input is exhausted.
func (s *scanner) atEOF() bool {
	s.ws()
	return s.i >= len(s.b)
}

// expect consumes byte c after whitespace.
func (s *scanner) expect(c byte, msg string) bool {
	if s.ws() != c {
		return s.syntax(msg)
	}
	s.i++
	return true
}

// literal consumes a keyword (true, false, null) starting at s.i.
func (s *scanner) literal(word string) bool {
	if len(s.b)-s.i < len(word) || string(s.b[s.i:s.i+len(word)]) != word {
		return s.syntax("invalid literal")
	}
	s.i += len(word)
	return true
}

// str is a scanned JSON string: raw bytes between the quotes plus metadata.
type str struct {
	raw   []byte // undecoded content (escapes intact)
	esc   bool   // raw contains at least one escape
	runes int    // number of Unicode code points after decoding
}

// readString consumes a string token; the opening quote must be at s.i.
func (s *scanner) readString() (str, bool) {
	s.i++ // opening quote
	start, runes, esc := s.i, 0, false
	for s.i < len(s.b) {
		c := s.b[s.i]
		switch {
		case c == '"':
			out := str{raw: s.b[start:s.i], esc: esc, runes: runes}
			s.i++
			return out, true
		case c == '\\':
			if !s.escape() {
				return str{}, false
			}
			esc = true
		case c < 0x20:
			return str{}, s.syntax("control character in string")
		case c < utf8.RuneSelf:
			s.i++
		default:
			r, n := utf8.DecodeRune(s.b[s.i:])
			if r == utf8.RuneError && n <= 1 {
				return str{}, s.syntax("invalid UTF-8 in string")
			}
			s.i += n
		}
		runes++
	}
	return str{}, s.syntax("unterminated string")
}

// escape consumes one escape sequence at s.i ('\\'); a surrogate pair counts
// as one sequence.
func (s *scanner) escape() bool {
	if s.i+1 >= len(s.b) {
		return s.syntax("unterminated string")
	}
	switch s.b[s.i+1] {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		s.i += 2
		return true
	case 'u':
	default:
		return s.syntax("invalid escape")
	}
	r, ok := hex4(s.b, s.i+2)
	if !ok {
		return s.syntax("invalid \\u escape")
	}
	s.i += 6
	if !utf16.IsSurrogate(r) {
		return true
	}
	if r < 0xDC00 && s.i+1 < len(s.b) && s.b[s.i] == '\\' && s.b[s.i+1] == 'u' {
		if lo, ok := hex4(s.b, s.i+2); ok && lo >= 0xDC00 && lo <= 0xDFFF {
			s.i += 6
			return true
		}
	}
	return s.syntax("lone UTF-16 surrogate escape")
}

// hex4 decodes four hex digits at b[i:] (strconv with an explicit base
// accepts no sign, prefix or underscore).
func hex4(b []byte, i int) (rune, bool) {
	if len(b)-i < 4 {
		return 0, false
	}
	r, err := strconv.ParseUint(string(b[i:i+4]), 16, 16)
	return rune(r), err == nil
}

// readNumber consumes a number token per the RFC 8259 grammar and reports
// whether it is written without fraction or exponent.
func (s *scanner) readNumber() (raw []byte, plainInt, ok bool) {
	start := s.i
	s.skip('-')
	switch {
	case s.skip('0'):
	case s.i < len(s.b) && s.b[s.i] >= '1' && s.b[s.i] <= '9':
		s.digits()
	case s.i >= len(s.b):
		return nil, false, s.syntax("unexpected end of input")
	default:
		return nil, false, s.syntax("expected value")
	}
	frac := s.skip('.')
	if frac && s.digits() == 0 {
		return nil, false, s.syntax("invalid number")
	}
	exp := s.skip('e') || s.skip('E')
	if exp && !s.skip('+') {
		s.skip('-')
	}
	if exp && s.digits() == 0 {
		return nil, false, s.syntax("invalid number")
	}
	return s.b[start:s.i], !frac && !exp, true
}

// skip consumes c if it is the next byte.
func (s *scanner) skip(c byte) bool {
	if s.i < len(s.b) && s.b[s.i] == c {
		s.i++
		return true
	}
	return false
}

func (s *scanner) digits() int {
	n := 0
	for s.i < len(s.b) && s.b[s.i] >= '0' && s.b[s.i] <= '9' {
		s.i++
		n++
	}
	return n
}

// unescape appends the decoded form of a valid scanned string to dst.
func unescape(dst, raw []byte) []byte {
	for i := 0; i < len(raw); {
		c := raw[i]
		if c != '\\' {
			dst = append(dst, c)
			i++
			continue
		}
		switch raw[i+1] {
		case 'b':
			dst = append(dst, '\b')
		case 'f':
			dst = append(dst, '\f')
		case 'n':
			dst = append(dst, '\n')
		case 'r':
			dst = append(dst, '\r')
		case 't':
			dst = append(dst, '\t')
		case 'u':
			r, _ := hex4(raw, i+2)
			if utf16.IsSurrogate(r) {
				lo, _ := hex4(raw, i+8)
				r = utf16.DecodeRune(r, lo)
				i += 6
			}
			dst = utf8.AppendRune(dst, r)
			i += 6
			continue
		default: // '"', '\\', '/'
			dst = append(dst, raw[i+1])
		}
		i += 2
	}
	return dst
}
