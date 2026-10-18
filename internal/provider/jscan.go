package provider

import (
	"errors"
	"math"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// errJSON is a malformed chunk; the caller falls back to encoding/json
// diagnostics only on this cold path.
var errJSON = errors.New("malformed JSON chunk")

// jscan is a minimal allocation-free JSON reader over one SSE data frame.
// Strings are returned as slices of the frame, or of out (unescaped) when
// they hold escapes or invalid UTF-8 (replaced by U+FFFD like
// encoding/json). out must have capacity ≥ len(b) so appends never move it.
type jscan struct {
	b     []byte
	i     int
	out   []byte
	depth int // open objects/arrays around the cursor (PROV-B1)
}

// maxDepth is the nesting limit of encoding/json (maxNestingDepth): deeper
// frames are errJSON. Nothing in this reader recurses on input structure
// (skip is iterative), so a 4 MiB frame of '[' costs no stack (PROV-B1).
const maxDepth = 10000

// open enters one object/array level.
func (s *jscan) open() error {
	if s.depth >= maxDepth {
		return errJSON
	}
	s.depth++
	return nil
}

func (s *jscan) ws() {
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case ' ', '\t', '\n', '\r':
			s.i++
		default:
			return
		}
	}
}

// peek returns the next non-space byte (0 at end).
func (s *jscan) peek() byte {
	s.ws()
	if s.i < len(s.b) {
		return s.b[s.i]
	}
	return 0
}

func (s *jscan) eat(c byte) bool {
	if s.peek() == c {
		s.i++
		return true
	}
	return false
}

func (s *jscan) lit(word string) error {
	if len(s.b)-s.i < len(word) || string(s.b[s.i:s.i+len(word)]) != word {
		return errJSON
	}
	s.i += len(word)
	return nil
}

// null consumes a null literal if present.
func (s *jscan) null() bool {
	if s.peek() == 'n' && s.lit("null") == nil {
		return true
	}
	return false
}

// object iterates the members of an object, calling f with each key
// (aliasing the frame or out) positioned before the value. null is an
// empty object (encoding/json decodes null into a map as nil).
func (s *jscan) object(f func(key []byte) error) error {
	if s.null() {
		return nil
	}
	if !s.eat('{') {
		return errJSON
	}
	if err := s.open(); err != nil {
		return err
	}
	if s.eat('}') {
		s.depth--
		return nil
	}
	for {
		key, err := s.str()
		if err != nil {
			return err
		}
		if !s.eat(':') {
			return errJSON
		}
		if err := f(key); err != nil {
			return err
		}
		if s.eat('}') {
			s.depth--
			return nil
		}
		if !s.eat(',') {
			return errJSON
		}
	}
}

// array iterates the elements of an array; null is an empty array.
func (s *jscan) array(f func(i int) error) error {
	if s.null() {
		return nil
	}
	if !s.eat('[') {
		return errJSON
	}
	if err := s.open(); err != nil {
		return err
	}
	if s.eat(']') {
		s.depth--
		return nil
	}
	for n := 0; ; n++ {
		if err := f(n); err != nil {
			return err
		}
		if s.eat(']') {
			s.depth--
			return nil
		}
		if !s.eat(',') {
			return errJSON
		}
	}
}

// str reads a string value.
func (s *jscan) str() ([]byte, error) {
	if !s.eat('"') {
		return nil, errJSON
	}
	start, plain := s.i, true
	for s.i < len(s.b) {
		c := s.b[s.i]
		switch {
		case c == '"':
			raw := s.b[start:s.i]
			s.i++
			if plain && utf8.Valid(raw) {
				return raw, nil
			}
			return s.unescape(raw)
		case c == '\\':
			plain = false
			s.i += 2
		case c < 0x20:
			return nil, errJSON
		default:
			s.i++
		}
	}
	return nil, errJSON
}

func (s *jscan) unescape(raw []byte) ([]byte, error) {
	start := len(s.out)
	for i := 0; i < len(raw); {
		c := raw[i]
		if c != '\\' {
			r, n := utf8.DecodeRune(raw[i:])
			s.out = utf8.AppendRune(s.out, r) // RuneError for invalid bytes
			i += n
			continue
		}
		if i+1 >= len(raw) {
			return nil, errJSON
		}
		i += 2
		switch raw[i-1] {
		case '"', '\\', '/':
			s.out = append(s.out, raw[i-1])
		case 'b':
			s.out = append(s.out, '\b')
		case 'f':
			s.out = append(s.out, '\f')
		case 'n':
			s.out = append(s.out, '\n')
		case 'r':
			s.out = append(s.out, '\r')
		case 't':
			s.out = append(s.out, '\t')
		case 'u':
			r, n, ok := hex4pair(raw[i:])
			if !ok {
				return nil, errJSON
			}
			s.out = utf8.AppendRune(s.out, r)
			i += n
		default:
			return nil, errJSON
		}
	}
	return s.out[start:len(s.out):len(s.out)], nil
}

// hex4pair decodes the XXXX of \uXXXX (b starts after "\u"), joining a
// following low surrogate; lone surrogates become U+FFFD (encoding/json).
func hex4pair(b []byte) (rune, int, bool) {
	r, ok := hex4(b)
	if !ok {
		return 0, 0, false
	}
	if !utf16.IsSurrogate(r) {
		return r, 4, true
	}
	if len(b) >= 10 && b[4] == '\\' && b[5] == 'u' {
		if r2, ok2 := hex4(b[6:]); ok2 {
			if d := utf16.DecodeRune(r, r2); d != utf8.RuneError {
				return d, 10, true
			}
		}
	}
	return utf8.RuneError, 4, true
}

func hex4(b []byte) (rune, bool) {
	if len(b) < 4 {
		return 0, false
	}
	var r rune
	for _, c := range b[:4] {
		switch {
		case c >= '0' && c <= '9':
			c -= '0'
		case c >= 'a' && c <= 'f':
			c -= 'a' - 10
		case c >= 'A' && c <= 'F':
			c -= 'A' - 10
		default:
			return 0, false
		}
		r = r<<4 | rune(c)
	}
	return r, true
}

// skip consumes any value and returns its raw bytes. It is iterative
// (PROV-B1): nesting costs a bit in kinds, never a stack frame.
func (s *jscan) skip() ([]byte, error) {
	s.ws()
	from, base := s.i, s.depth
	var kinds kindStack
	err := s.skipLoop(base, &kinds)
	if err != nil {
		s.depth = base
	}
	return s.b[from:s.i], err
}

func (s *jscan) skipLoop(base int, kinds *kindStack) error {
	for {
		descend, err := s.valueStart(base, kinds)
		if err != nil {
			return err
		}
		if descend {
			continue // positioned at the first member's value
		}
		done, err := s.afterValue(base, kinds)
		if done || err != nil {
			return err
		}
	}
}

// valueStart consumes a scalar or an empty container (false), or opens a
// non-empty container and positions at its first value (true).
func (s *jscan) valueStart(base int, kinds *kindStack) (bool, error) {
	c := s.peek()
	if c != '{' && c != '[' {
		return false, s.scalar(c)
	}
	s.i++
	if err := s.open(); err != nil {
		return false, err
	}
	obj := c == '{'
	kinds.set(s.depth-base, obj)
	if s.eat(closer(obj)) {
		s.depth--
		return false, nil
	}
	return true, s.memberKey(obj)
}

// afterValue closes finished containers; it stops at a ',' (positioned at
// the next value, after its key in an object) or when the skipped value is
// complete (done).
func (s *jscan) afterValue(base int, kinds *kindStack) (done bool, err error) {
	for s.depth > base {
		obj := kinds.get(s.depth - base)
		if s.eat(',') {
			return false, s.memberKey(obj)
		}
		if !s.eat(closer(obj)) {
			return false, errJSON
		}
		s.depth--
	}
	return true, nil
}

// memberKey consumes `"key":` inside an object; arrays have none.
func (s *jscan) memberKey(obj bool) error {
	if !obj {
		return nil
	}
	if _, err := s.str(); err != nil {
		return err
	}
	if !s.eat(':') {
		return errJSON
	}
	return nil
}

func (s *jscan) scalar(c byte) error {
	var err error
	switch c {
	case '"':
		_, err = s.str()
	case 't':
		err = s.lit("true")
	case 'f':
		err = s.lit("false")
	case 'n':
		err = s.lit("null")
	default:
		_, err = s.number()
	}
	return err
}

func closer(obj bool) byte {
	if obj {
		return '}'
	}
	return ']'
}

// kindStack holds one bit per open container level ≥ 1 (1 = object): a
// word for the first 64 levels, a slice beyond (deep input only).
type kindStack struct {
	w    uint64
	more []uint64
}

func (k *kindStack) set(level int, obj bool) {
	i, bit := (level-1)/64, uint64(1)<<((level-1)%64)
	word := &k.w
	if i > 0 {
		for len(k.more) < i {
			k.more = append(k.more, 0)
		}
		word = &k.more[i-1]
	}
	if obj {
		*word |= bit
	} else {
		*word &^= bit
	}
}

func (k *kindStack) get(level int) bool {
	i, bit := (level-1)/64, uint64(1)<<((level-1)%64)
	if i == 0 {
		return k.w&bit != 0
	}
	return k.more[i-1]&bit != 0
}

// number reads a JSON number (RFC 8259 grammar) as float64, correctly
// rounded like strconv.ParseFloat: exact fast path (mantissa < 2^53,
// |exp| ≤ 22), else ParseFloat on the literal (rare; may allocate).
func (s *jscan) number() (float64, error) {
	s.ws()
	start := s.i
	neg := s.at('-')
	mant, n := s.digits(0)
	if n == 0 || n > 1 && s.b[s.i-n] == '0' {
		return 0, errJSON // no digits or a leading zero
	}
	exp := 0
	if s.at('.') {
		if mant, n = s.digits(mant); n == 0 {
			return 0, errJSON
		}
		exp = -n
	}
	e, err := s.exponent()
	if err != nil {
		return 0, err
	}
	return s.compose(mant, exp+e, start, neg)
}

// exponent reads an optional e/E exponent (clamped to ±1000).
func (s *jscan) exponent() (int, error) {
	if !s.at('e') && !s.at('E') {
		return 0, nil
	}
	sign := 1.0
	if s.at('-') {
		sign = -1
	} else {
		s.at('+')
	}
	e, n := s.digits(0)
	if n == 0 {
		return 0, errJSON
	}
	return int(sign * min(e, 1000)), nil
}

// compose returns mant × 10^exp for the literal b[start:i].
func (s *jscan) compose(mant float64, exp, start int, neg bool) (float64, error) {
	var v float64
	switch {
	case mant < 1<<53 && exp >= 0 && exp <= 22:
		v = mant * math.Pow10(exp)
	case mant < 1<<53 && exp < 0 && exp >= -22:
		v = mant / math.Pow10(-exp)
	default:
		f, err := strconv.ParseFloat(string(s.b[start:s.i]), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return 0, errJSON
		}
		return f, nil
	}
	if neg {
		v = -v
	}
	return v, nil
}

// at consumes byte c if it is next (no whitespace skipping).
func (s *jscan) at(c byte) bool {
	if s.i < len(s.b) && s.b[s.i] == c {
		s.i++
		return true
	}
	return false
}

// digits accumulates a run of decimal digits onto v; n = digits read.
func (s *jscan) digits(v float64) (float64, int) {
	n := 0
	for ; s.i < len(s.b) && s.b[s.i] >= '0' && s.b[s.i] <= '9'; s.i++ {
		v = v*10 + float64(s.b[s.i]-'0')
		n++
	}
	return v, n
}

// int reads a non-negative integer that fits int64 (token counts).
func (s *jscan) int() (int64, error) {
	v, err := s.number()
	if err != nil || v < 0 || v != math.Trunc(v) || v > 1<<53 {
		return 0, errJSON
	}
	return int64(v), nil
}
