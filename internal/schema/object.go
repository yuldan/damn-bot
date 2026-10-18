package schema

import (
	"strconv"
	"strings"
)

// maxListedProps bounds the property names listed in an
// additionalProperties message.
const maxListedProps = 20

// objFrame is the per-object state of object validation.
type objFrame struct {
	n    *node
	bits int    // offset of this object's "property seen" bitset in state.bits
	keys keySet // names outside properties (declared ones use bits)
}

// object validates an object against n (non-nil); '{' is at the cursor.
func (st *state) object(n *node, depth int) bool {
	at := st.s.i
	if depth >= MaxDepth {
		return st.s.depthError()
	}
	st.s.i++
	f := objFrame{n: n, bits: len(st.bits), keys: st.s.openKeys()}
	st.bits = append(st.bits, make([]uint64, (len(n.props)+63)/64)...)
	ok := st.members(&f, depth) && st.requiredPresent(&f, at)
	st.bits = st.bits[:f.bits]
	st.s.closeKeys(f.keys)
	return ok
}

// members consumes "key: value" pairs up to and including '}'.
func (st *state) members(f *objFrame, depth int) bool {
	for first := true; ; first = false {
		k, at, done, ok := st.s.nextKey(first)
		if done || !ok {
			return ok
		}
		st.path = append(st.path, seg{key: k.raw, esc: k.esc, idx: -1})
		ok = st.member(f, keyName(k), at, depth)
		st.path = st.path[:len(st.path)-1]
		if !ok {
			return false
		}
	}
}

// member checks one member name for duplicates and validates its value.
func (st *state) member(f *objFrame, name []byte, at, depth int) bool {
	idx, declared := f.n.index[string(name)]
	if declared {
		w, bit := f.bits+idx/64, uint64(1)<<(idx%64)
		if st.bits[w]&bit != 0 {
			st.s.i = at
			return st.s.syntax("duplicate object key " + strconv.Quote(string(name)))
		}
		st.bits[w] |= bit
		if p := &f.n.props[idx]; p.declared {
			return st.value(p.schema, depth+1)
		}
		// Listed only in required: otherwise governed by additionalProperties.
	} else if !st.s.unique(&f.keys, name, at) {
		return false
	}
	if f.n.noAddl {
		return st.violate(at, "additionalProperties", func() string { return unexpectedMsg(f.n, name) })
	}
	return st.value(f.n.addl, depth+1)
}

func (st *state) requiredPresent(f *objFrame, at int) bool {
	for _, i := range f.n.required {
		if st.bits[f.bits+i/64]&(uint64(1)<<(i%64)) == 0 {
			name := f.n.props[i].name
			return st.violate(at, "required", func() string { return "missing required property " + strconv.Quote(name) })
		}
	}
	return true
}

func unexpectedMsg(n *node, name []byte) string {
	var allowed []string
	for _, p := range n.props {
		if p.declared && len(allowed) < maxListedProps {
			allowed = append(allowed, p.name)
		}
	}
	msg := "unexpected property " + strconv.Quote(string(name))
	if len(allowed) > 0 {
		msg += " (allowed: " + strings.Join(allowed, ", ") + ")"
	}
	return msg
}
