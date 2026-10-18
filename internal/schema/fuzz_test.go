package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"testing"
	"unicode/utf8"
)

// FuzzToolArgs (SPEC P3, ENGINEERING E-57): arbitrary tool arguments never
// crash the validator, and every verdict agrees with an independent oracle
// built on encoding/json: accepted ⇒ the oracle accepts; a keyword violation
// ⇒ valid JSON the oracle rejects; invalid JSON ⇒ a syntax error.
func FuzzToolArgs(f *testing.F) {
	for _, c := range append(append(validCases(), syntaxCases()...), violationCases()...) {
		f.Add([]byte(c.doc))
	}
	s := mustCompile(f, fixtureSchema, ModeInternal)
	var ref any
	if err := json.Unmarshal([]byte(fixtureSchema), &ref); err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, doc []byte) {
		if msg := checkVerdict(t, s.Validate(doc), ref, doc); msg != "" {
			t.Fatalf("%s: %q", msg, doc)
		}
	})
}

// checkVerdict returns a description of the disagreement, or "".
func checkVerdict(t *testing.T, err error, ref any, doc []byte) string {
	t.Helper()
	var ve *Error
	if err != nil && (!errors.As(err, &ve) || ve.Offset < 0 || ve.Offset > len(doc)) {
		return "malformed error " + err.Error()
	}
	valid := json.Valid(doc)
	switch {
	case err == nil && (!valid || !refValid(ref, decodeRef(t, doc))):
		return "accepted, oracle rejects"
	case err == nil:
		return ""
	case ve.Kind == KindViolation && (!valid || refValid(ref, decodeRef(t, doc))):
		return "violation " + err.Error() + ", oracle accepts or JSON invalid"
	}
	// Syntax/depth errors on JSON that encoding/json accepts are the stricter
	// rules: duplicate keys, invalid UTF-8, lone surrogates, MaxDepth.
	return ""
}

func decodeRef(t *testing.T, doc []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("oracle decode %q: %v", doc, err)
	}
	return v
}

// refValid is a deliberately naive reference validator for the keyword
// subset over encoding/json values (schemas in this test are ModeInternal).
func refValid(schema, v any) bool {
	if b, isBool := schema.(bool); isBool {
		return b
	}
	sc, isMap := schema.(map[string]any)
	if !isMap {
		return false
	}
	if ts, has := sc["type"]; has && !refType(ts, v) {
		return false
	}
	return refEnum(sc, v) && refNumber(sc, v) && refString(sc, v) && refArray(sc, v) && refObject(sc, v)
}

func refType(ts, v any) bool {
	list, isList := ts.([]any)
	if !isList {
		list = []any{ts}
	}
	for _, x := range list {
		if name, isStr := x.(string); isStr && refTypeMatches(name, v) {
			return true
		}
	}
	return false
}

func refTypeMatches(name string, v any) bool {
	if f, isNum := asNum(v); isNum {
		return name == "number" || (name == "integer" && !math.IsInf(f, 0) && f == math.Trunc(f))
	}
	return refTypeName(v) == name
}

// refTypeName names the non-numeric JSON types.
func refTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return ""
}

func refNum(n json.Number) float64 {
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return math.NaN()
	}
	return f
}

// asNum maps schema numbers (float64) and instance numbers (json.Number)
// to one representation.
func asNum(x any) (float64, bool) {
	switch n := x.(type) {
	case float64:
		return n, true
	case json.Number:
		return refNum(n), true
	}
	return 0, false
}

func refEnum(sc map[string]any, v any) bool {
	if c, has := sc["const"]; has && !refEqual(c, v) {
		return false
	}
	e, has := sc["enum"].([]any)
	if !has {
		return true
	}
	for _, x := range e {
		if refEqual(x, v) {
			return true
		}
	}
	return false
}

func refEqual(a, b any) bool {
	if x, isNum := asNum(a); isNum {
		y, both := asNum(b)
		return both && x == y
	}
	switch av := a.(type) {
	case []any:
		bv, same := b.([]any)
		if !same || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !refEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bv, same := b.(map[string]any)
		return same && refEqualObject(av, bv)
	}
	return a == b
}

func refEqualObject(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, x := range a {
		if y, has := b[k]; !has || !refEqual(x, y) {
			return false
		}
	}
	return true
}

// refBound reads a numeric keyword from an encoding/json schema.
func refBound(sc map[string]any, key string) (float64, bool) {
	f, has := sc[key].(float64)
	return f, has
}

func refNumber(sc map[string]any, v any) bool {
	f, isNum := asNum(v)
	if !isNum {
		return true
	}
	if lo, has := refBound(sc, "minimum"); has && !(f >= lo) {
		return false
	}
	hi, has := refBound(sc, "maximum")
	return !has || f <= hi
}

func refString(sc map[string]any, v any) bool {
	s, isStr := v.(string)
	if !isStr {
		return true
	}
	n := float64(utf8.RuneCountInString(s))
	if lo, has := refBound(sc, "minLength"); has && n < lo {
		return false
	}
	if hi, has := refBound(sc, "maxLength"); has && n > hi {
		return false
	}
	p, has := sc["pattern"].(string)
	return !has || regexp.MustCompile(p).MatchString(s)
}

func refArray(sc map[string]any, v any) bool {
	a, isArr := v.([]any)
	if !isArr {
		return true
	}
	n := float64(len(a))
	if lo, has := refBound(sc, "minItems"); has && n < lo {
		return false
	}
	if hi, has := refBound(sc, "maxItems"); has && n > hi {
		return false
	}
	items, has := sc["items"]
	for _, e := range a {
		if has && !refValid(items, e) {
			return false
		}
	}
	return true
}

func refObject(sc map[string]any, v any) bool {
	o, isObj := v.(map[string]any)
	if !isObj {
		return true
	}
	if req, has := sc["required"].([]any); has {
		for _, r := range req {
			name, isStr := r.(string)
			if _, present := o[name]; !isStr || !present {
				return false
			}
		}
	}
	props, isMap := sc["properties"].(map[string]any)
	if !isMap {
		props = nil
	}
	for k, x := range o {
		ps, declared := props[k]
		ap, hasAP := sc["additionalProperties"]
		if declared && !refValid(ps, x) || !declared && hasAP && !refValid(ap, x) {
			return false
		}
	}
	return true
}
