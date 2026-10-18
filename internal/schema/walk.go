package schema

import (
	"bytes"
	"slices"
	"strconv"
)

// linearKeys is the object size up to which duplicate detection scans the
// member list instead of building a set.
const linearKeys = 16

// check reports the first strict-JSON violation of doc (RFC 8259 syntax,
// valid UTF-8, unique member names, nesting ≤ MaxDepth), or nil.
func check(doc []byte) *Error {
	s := scanner{b: doc}
	if s.skipValue(0) && !s.atEOF() {
		s.syntax("unexpected data after top-level value")
	}
	return s.err
}

// skipValue consumes one value under the strict rules without a schema.
func (s *scanner) skipValue(depth int) bool {
	switch c := s.ws(); c {
	case '{', '[':
		if depth >= MaxDepth {
			return s.depthError()
		}
		s.i++
		if c == '{' {
			return s.skipObject(depth)
		}
		return s.skipArray(depth)
	case '"':
		_, ok := s.readString()
		return ok
	case 't':
		return s.literal("true")
	case 'f':
		return s.literal("false")
	case 'n':
		return s.literal("null")
	}
	_, _, ok := s.readNumber()
	return ok
}

func (s *scanner) skipObject(depth int) bool {
	ks := s.openKeys()
	defer s.closeKeys(ks)
	for first := true; ; first = false {
		k, at, done, ok := s.nextKey(first)
		if done || !ok {
			return ok
		}
		if !s.unique(&ks, keyName(k), at) || !s.skipValue(depth+1) {
			return false
		}
	}
}

func (s *scanner) skipArray(depth int) bool {
	for first := true; ; first = false {
		if done, ok := s.nextElem(first); done || !ok {
			return ok
		}
		if !s.skipValue(depth + 1) {
			return false
		}
	}
}

// nextKey advances inside an object whose '{' is consumed: it consumes '}'
// (done) or the separator, the next key and its ':'. at is the key offset.
func (s *scanner) nextKey(first bool) (k str, at int, done, ok bool) {
	if s.ws() == '}' {
		s.i++
		return str{}, 0, true, true
	}
	if !first && !s.expect(',', "expected ',' or '}'") {
		return str{}, 0, false, false
	}
	if s.ws() != '"' {
		return str{}, 0, false, s.syntax("expected object key")
	}
	at = s.i
	if k, ok = s.readString(); !ok {
		return str{}, 0, false, false
	}
	return k, at, false, s.expect(':', "expected ':' after object key")
}

// nextElem advances inside an array whose '[' is consumed: it consumes ']'
// (done) or, after the first element, the separator.
func (s *scanner) nextElem(first bool) (done, ok bool) {
	if s.ws() == ']' {
		s.i++
		return true, true
	}
	return false, first || s.expect(',', "expected ',' or ']'")
}

// keyName returns the decoded member name (a copy only when escaped).
func keyName(k str) []byte {
	if !k.esc {
		return k.raw
	}
	return unescape(make([]byte, 0, len(k.raw)), k.raw) // decoded is never longer
}

// keySet tracks the member names of one object. Names live on the
// scanner's shared stack, so nested small objects allocate nothing; an
// object with more than linearKeys names switches to a set (O(n) overall).
type keySet struct {
	from int
	seen map[string]struct{}
}

func (s *scanner) openKeys() keySet { return keySet{from: len(s.keys)} }

func (s *scanner) closeKeys(ks keySet) {
	clear(s.keys[ks.from:]) // drop references into the document
	s.keys = s.keys[:ks.from]
}

// unique records name (found at offset at) and fails with a syntax error if
// the object already has it.
func (s *scanner) unique(ks *keySet, name []byte, at int) bool {
	names := s.keys[ks.from:]
	dup := false
	switch {
	case ks.seen == nil && len(names) < linearKeys:
		dup = slices.ContainsFunc(names, func(prev []byte) bool { return bytes.Equal(prev, name) })
		s.keys = append(s.keys, name)
	default:
		if ks.seen == nil {
			ks.seen = make(map[string]struct{}, 2*len(names))
			for _, prev := range names {
				ks.seen[string(prev)] = struct{}{}
			}
		}
		_, dup = ks.seen[string(name)]
		ks.seen[string(name)] = struct{}{}
	}
	if dup {
		s.i = at
		return s.syntax("duplicate object key " + strconv.Quote(string(name)))
	}
	return true
}

func (s *scanner) depthError() bool {
	s.syntax("nesting deeper than " + strconv.Itoa(MaxDepth))
	s.err.Kind = KindDepth
	return false
}
