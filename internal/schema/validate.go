package schema

import (
	"math"
	"slices"
	"strconv"
	"strings"
)

// maxRetainedScratch caps the scratch buffer kept in the pool between calls.
const maxRetainedScratch = 64 << 10

// seg is one JSON Pointer step: an object key (raw, possibly escaped) or an
// array index.
type seg struct {
	key []byte
	esc bool
	idx int // ≥ 0 for array elements
}

// state is per-call scratch. Validation walks the document once with the
// scanner and checks keywords as tokens pass, so the success path does not
// build any value tree.
type state struct {
	s     scanner
	path  []seg
	bits  []uint64 // stacked "property seen" bitsets, one frame per open object
	buf   []byte   // unescape scratch
	quiet int      // > 0 inside an anyOf trial: failures are not described
	fail  *Error
}

// Validate checks doc against the schema. It returns nil or an *Error
// (errors.Is(err, ErrInvalid)) describing the first failure. Syntax errors
// take precedence over keyword violations: the model must fix the JSON
// first, wherever in the document the syntax error is.
func (sc *Schema) Validate(doc []byte) error {
	st, ok := sc.pool.Get().(*state)
	if !ok {
		st = &state{}
	}
	defer sc.release(st)
	e := st.run(sc.root, doc)
	if e == nil {
		return nil
	}
	if e.Kind == KindViolation {
		// Validation stopped early; the rest may still be malformed (cold path).
		if se := check(doc); se != nil {
			return se
		}
	}
	return e
}

// run validates doc against n from a clean state.
func (st *state) run(n *node, doc []byte) *Error {
	st.reset(doc)
	if st.value(n, 0) && !st.s.atEOF() {
		st.s.syntax("unexpected data after top-level value")
	}
	if st.s.err != nil {
		return st.s.err
	}
	return st.fail
}

// reset prepares st for doc and drops references into the previous
// document, so pooled scratch never retains a caller's buffer.
func (st *state) reset(doc []byte) {
	clear(st.path[:cap(st.path)])
	clear(st.s.keys[:cap(st.s.keys)])
	*st = state{s: scanner{b: doc, keys: st.s.keys[:0]}, path: st.path[:0], bits: st.bits[:0], buf: st.buf[:0]}
}

func (sc *Schema) release(st *state) {
	st.reset(nil)
	if cap(st.buf) <= maxRetainedScratch {
		sc.pool.Put(st)
	}
}

// violate records a keyword failure at the current path. msg is evaluated
// only when the failure is reported (never inside anyOf trials).
func (st *state) violate(at int, keyword string, msg func() string) bool {
	if st.quiet == 0 && st.fail == nil {
		st.fail = &Error{Kind: KindViolation, Offset: at, Pointer: st.pointer(), Keyword: keyword, Msg: msg()}
	}
	return false
}

func (st *state) pointer() string {
	var b strings.Builder
	for _, p := range st.path {
		b.WriteByte('/')
		if p.idx >= 0 {
			b.WriteString(strconv.Itoa(p.idx))
			continue
		}
		b.WriteString(escapePointer(string(keyName(str{raw: p.key, esc: p.esc}))))
	}
	return b.String()
}

// value validates the next value against n (nil = any) and consumes it.
func (st *state) value(n *node, depth int) bool {
	if n == nil {
		return st.s.skipValue(depth)
	}
	c := st.s.ws()
	at := st.s.i
	if n.never {
		return st.violate(at, "false", func() string { return "no value is allowed here" })
	}
	if n.anyOf != nil && !st.anyOf(n, depth) {
		return false
	}
	var ok bool
	switch c {
	case '{':
		ok = st.typed(n, tObject, at) && st.object(n, depth)
	case '[':
		ok = st.typed(n, tArray, at) && st.array(n, depth)
	case '"':
		return st.str(n, at) // checks enum/const itself, allocation-free
	case 't', 'f', 'n':
		ok = st.literal(n, c, at)
	default:
		ok = st.number(n, at)
	}
	if !ok || len(n.enums) == 0 {
		return ok
	}
	return st.enumValue(n, at)
}

func (st *state) literal(n *node, c byte, at int) bool {
	switch c {
	case 't':
		return st.s.literal("true") && st.typed(n, tBool, at)
	case 'f':
		return st.s.literal("false") && st.typed(n, tBool, at)
	}
	return st.s.literal("null") && st.typed(n, tNull, at)
}

// typed checks the type keyword for a value of type t.
func (st *state) typed(n *node, t typeMask, at int) bool {
	if n.types == 0 || n.types&t != 0 {
		return true
	}
	return st.violate(at, "type", func() string { return "expected " + maskNames(n.types) + ", got " + maskNames(t) })
}

func maskNames(m typeMask) string {
	var parts []string
	for i, name := range typeNames() {
		if m&(1<<i) != 0 {
			parts = append(parts, name)
		}
	}
	return strings.Join(parts, " or ")
}

// anyOf tries each branch on the value without consuming it.
func (st *state) anyOf(n *node, depth int) bool {
	start := st.s.i
	st.quiet++
	matched := slices.ContainsFunc(n.anyOf, func(b *node) bool {
		st.s.i = start
		return st.value(b, depth) || st.s.err != nil // syntax errors are final
	})
	st.quiet--
	st.s.i = start
	switch {
	case st.s.err != nil:
		return false
	case !matched:
		return st.violate(start, "anyOf", func() string { return "value does not match any schema in anyOf" })
	}
	return true
}

func (st *state) number(n *node, at int) bool {
	raw, plainInt, ok := st.s.readNumber()
	if !ok {
		return false
	}
	var f float64
	if !plainInt || n.hasMin || n.hasMax {
		f = parseNum(string(raw))
	}
	isInt := plainInt || (!math.IsInf(f, 0) && f == math.Trunc(f))
	switch {
	case !st.numberType(n, isInt, at):
		return false
	case n.hasMin && !(f >= n.min): // NaN fails: fail closed
		return st.violate(at, "minimum", func() string { return "must be >= " + fmtNum(n.min) })
	case n.hasMax && !(f <= n.max):
		return st.violate(at, "maximum", func() string { return "must be <= " + fmtNum(n.max) })
	}
	return true
}

// numberType: "number" admits every number, "integer" those with a zero
// fractional part (1.0 included, draft 2020-12).
func (st *state) numberType(n *node, isInt bool, at int) bool {
	if n.types == 0 || n.types&tNumber != 0 || (isInt && n.types&tInteger != 0) {
		return true
	}
	t := tNumber
	if isInt {
		t = tInteger
	}
	return st.typed(n, t, at)
}

func fmtNum(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func (st *state) str(n *node, at int) bool {
	s, ok := st.s.readString()
	switch {
	case !ok || !st.typed(n, tString, at):
		return false
	case n.counts[minLength] >= 0 && s.runes < n.counts[minLength]:
		return st.violate(at, "minLength", func() string { return "must be at least " + strconv.Itoa(n.counts[minLength]) + " characters long" })
	case n.counts[maxLength] >= 0 && s.runes > n.counts[maxLength]:
		return st.violate(at, "maxLength", func() string { return "must be at most " + strconv.Itoa(n.counts[maxLength]) + " characters long" })
	case n.pattern == nil && len(n.enums) == 0:
		return true
	}
	dec := s.raw
	if s.esc {
		st.buf = unescape(st.buf[:0], s.raw)
		dec = st.buf
	}
	if n.pattern != nil && !n.pattern.Match(dec) {
		return st.violate(at, "pattern", func() string { return "must match pattern " + strconv.Quote(n.pattern.String()) })
	}
	for i := range n.enums {
		e := &n.enums[i]
		if e.strs == nil {
			return st.enumValue(n, at)
		}
		if !slices.ContainsFunc(e.strs, func(m string) bool { return m == string(dec) }) { // no conversion alloc
			return st.violateEnum(e, at)
		}
	}
	return true
}

// enumValue is the general (allocating, cold) enum/const check: it decodes
// the just-consumed, already strictly scanned value.
func (st *state) enumValue(n *node, at int) bool {
	v, err := decode(st.s.b[at:st.s.i])
	if err != nil {
		return st.s.syntax(err.Error()) // unreachable: bytes passed the strict scan
	}
	for i := range n.enums {
		if e := &n.enums[i]; !e.has(v) {
			return st.violateEnum(e, at)
		}
	}
	return true
}

func (st *state) violateEnum(e *enumSet, at int) bool {
	if e.isConst {
		return st.violate(at, "const", func() string { return "must be " + e.text })
	}
	return st.violate(at, "enum", func() string { return "must be one of [" + e.text + "]" })
}

// array validates an array against n (non-nil); '[' is at the cursor.
func (st *state) array(n *node, depth int) bool {
	at := st.s.i
	if depth >= MaxDepth {
		return st.s.depthError()
	}
	st.s.i++
	count := 0
	for ; ; count++ {
		done, ok := st.s.nextElem(count == 0)
		if !ok {
			return false
		}
		if done {
			break
		}
		st.path = append(st.path, seg{idx: count})
		ok = st.value(n.items, depth+1)
		st.path = st.path[:len(st.path)-1]
		if !ok {
			return false
		}
	}
	if lo := n.counts[minItems]; lo >= 0 && count < lo {
		return st.violate(at, "minItems", func() string { return "must contain at least " + strconv.Itoa(lo) + " items" })
	}
	if hi := n.counts[maxItems]; hi >= 0 && count > hi {
		return st.violate(at, "maxItems", func() string { return "must contain at most " + strconv.Itoa(hi) + " items" })
	}
	return true
}
