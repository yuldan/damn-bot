package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Cold paths (schema compilation, enum/const of non-string instances) decode
// with encoding/json after the strict scanner has accepted the bytes; the
// validation hot path never builds values.

// decode parses one JSON value that the strict scanner already accepted.
func decode(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("schema: decode: %w", err)
	}
	return v, nil
}

// parseNum converts a grammar-checked number. Out-of-range magnitudes become
// ±Inf (strconv semantics), which no finite bound accepts as integer.
func parseNum(raw string) float64 {
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return math.NaN() // unreachable after the grammar check; NaN fails every bound
	}
	return f
}

// enumSet is a compiled enum (or const as a one-member set).
type enumSet struct {
	vals    []any
	strs    []string // non-nil when every member is a string: allocation-free check
	isConst bool
	text    string // members as JSON, for messages
}

func newEnum(vals []any, isConst bool) enumSet {
	e := enumSet{vals: vals, isConst: isConst, strs: []string{}}
	parts := make([]string, len(vals))
	for i, v := range vals {
		if s, ok := v.(string); ok && e.strs != nil {
			e.strs = append(e.strs, s)
		} else {
			e.strs = nil
		}
		parts[i] = encode(v)
	}
	e.text = strings.Join(parts, ",")
	return e
}

// encode renders a decoded value as compact JSON without HTML escaping.
func encode(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v) // unreachable for decoded JSON values
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// ErrSchema matches every *CompileError returned by Compile.
var ErrSchema = errors.New("schema: invalid schema")

// CompileError reports an invalid or unsupported schema at a JSON Pointer.
type CompileError struct {
	Pointer     string
	Unsupported string // keyword outside the ModeInternal subset, when that is the reason
	Msg         string
}

// Error renders the schema location and reason.
func (e *CompileError) Error() string { return "schema " + orRoot(e.Pointer) + ": " + e.Msg }

// Is reports ErrSchema for every compile error.
func (e *CompileError) Is(target error) bool { return target == ErrSchema }
