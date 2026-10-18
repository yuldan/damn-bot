package schema

import (
	"encoding/json"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Mode selects how Compile treats keywords outside the subset (SPEC §4.4).
type Mode uint8

const (
	// ModeInternal is for Damn-Bot's own schemas: any keyword outside the
	// subset, and anyOf, is a compile error (TestSchemaKeywordsSupported).
	ModeInternal Mode = iota
	// ModeMCP is for third-party (MCP) schemas: unsupported keywords are
	// ignored and reported by Partial; oneOf is enforced as anyOf (ADR-095).
	ModeMCP
)

// typeMask is a set of JSON Schema types; 0 means any type.
type typeMask uint8

const (
	tNull typeMask = 1 << iota
	tBool
	tObject
	tArray
	tNumber
	tString
	tInteger
)

// typeNames lists type names ordered by bit position.
func typeNames() []string {
	return []string{"null", "boolean", "object", "array", "number", "string", "integer"}
}

// Indexes of node.counts (non-negative integer bounds; -1 = unset).
const (
	minLength = iota
	maxLength
	minItems
	maxItems
)

// node is one compiled (sub)schema. A nil *node accepts any value.
type node struct {
	never    bool // the false schema
	types    typeMask
	props    []prop
	index    map[string]int // property name → props index
	required []int          // indexes into props
	noAddl   bool           // additionalProperties: false
	addl     *node
	items    *node
	enums    []enumSet // enum and/or const; every set must contain the value
	hasMin   bool
	hasMax   bool
	min, max float64
	counts   [4]int
	pattern  *regexp.Regexp
	anyOf    []*node
}

type prop struct {
	name     string
	schema   *node
	declared bool // listed in properties (false: only in required)
}

// Schema is an immutable compiled schema, safe for concurrent Validate.
type Schema struct {
	root    *node
	partial []string
	pool    sync.Pool // *state scratch, reused across Validate calls
}

// Partial lists, sorted and unique, the keywords that were ignored or
// weakened while compiling an MCP schema; non-empty marks the tool
// schema_partial (SPEC §4.4). Always empty for ModeInternal.
func (s *Schema) Partial() []string { return slices.Clone(s.partial) }

// Compile parses and compiles a schema document. The document must be
// strict JSON (the same rules as Validate, including no duplicate keys).
func Compile(raw []byte, mode Mode) (*Schema, error) {
	if e := check(raw); e != nil {
		return nil, &CompileError{Msg: e.Error()}
	}
	v, err := decode(raw)
	if err != nil {
		return nil, &CompileError{Msg: err.Error()}
	}
	c := compiler{mode: mode, partial: map[string]bool{}}
	root, err := c.node(v, "")
	if err != nil {
		return nil, err
	}
	return &Schema{root: root, partial: slices.Sorted(maps.Keys(c.partial))}, nil
}

type compiler struct {
	mode    Mode
	partial map[string]bool
}

func (c *compiler) fail(ptr, msg string) error { return &CompileError{Pointer: ptr, Msg: msg} }

// node compiles the schema value v located at JSON Pointer ptr. Recursion
// is bounded by MaxDepth of the strict pre-scan.
func (c *compiler) node(v any, ptr string) (*node, error) {
	n := &node{counts: [4]int{-1, -1, -1, -1}}
	if b, ok := v.(bool); ok {
		n.never = !b
		return n, nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, c.fail(ptr, "schema must be an object or boolean")
	}
	for _, key := range slices.Sorted(maps.Keys(obj)) { // deterministic first error
		if key == "required" {
			continue // after properties: it refers to their entries
		}
		kp := ptr + "/" + escapePointer(key)
		known, err := c.structural(n, key, obj[key], kp)
		if !known && err == nil {
			known, err = c.assertion(n, key, obj[key], kp)
		}
		if !known && err == nil {
			err = c.unknown(key, kp)
		}
		if err != nil {
			return nil, err
		}
	}
	if req, ok := obj["required"]; ok {
		return n, c.required(n, req, ptr+"/required")
	}
	return n, nil
}

// structural compiles keywords that hold subschemas or shape.
func (c *compiler) structural(n *node, key string, v any, ptr string) (bool, error) {
	var err error
	switch key {
	case "type":
		n.types, err = c.types(v, ptr)
	case "properties":
		err = c.properties(n, v, ptr)
	case "additionalProperties":
		if b, ok := v.(bool); ok && !b {
			n.noAddl = true
		} else {
			n.addl, err = c.node(v, ptr)
		}
	case "items":
		if _, tuple := v.([]any); tuple { // tuple form: outside the subset
			return true, c.unknown("items", ptr)
		}
		n.items, err = c.node(v, ptr)
	case "anyOf", "oneOf":
		if c.mode == ModeInternal {
			return true, c.unsupported(key, ptr)
		}
		if key == "oneOf" {
			c.partial["oneOf"] = true // enforced as anyOf, i.e. weaker
		}
		err = c.anyOf(n, v, ptr)
	case "description":
		if _, ok := v.(string); !ok {
			err = c.fail(ptr, "description must be a string")
		}
	case "default":
	default:
		return false, nil
	}
	return true, err
}

// assertion compiles keywords that constrain a value directly.
func (c *compiler) assertion(n *node, key string, v any, ptr string) (bool, error) {
	switch key {
	case "enum":
		members, ok := v.([]any) // empty is valid and matches nothing (draft 2020-12)
		if !ok {
			return true, c.fail(ptr, "enum must be an array")
		}
		n.enums = append(n.enums, newEnum(members, false))
	case "const":
		n.enums = append(n.enums, newEnum([]any{v}, true))
	case "minimum", "maximum":
		f, ok := v.(json.Number)
		if !ok {
			return true, c.fail(ptr, key+" must be a number")
		}
		if key == "minimum" {
			n.hasMin, n.min = true, parseNum(string(f))
		} else {
			n.hasMax, n.max = true, parseNum(string(f))
		}
	case "minLength":
		return true, c.count(&n.counts[minLength], v, ptr)
	case "maxLength":
		return true, c.count(&n.counts[maxLength], v, ptr)
	case "minItems":
		return true, c.count(&n.counts[minItems], v, ptr)
	case "maxItems":
		return true, c.count(&n.counts[maxItems], v, ptr)
	case "pattern":
		return true, c.pattern(n, v, ptr)
	default:
		return false, nil
	}
	return true, nil
}

// isAnnotation: keywords without validation semantics; MCP schemas may use
// them without being marked partial.
func isAnnotation(key string) bool {
	switch key {
	case "title", "$schema", "$id", "$comment", "examples", "deprecated", "readOnly", "writeOnly":
		return true
	}
	return false
}

func (c *compiler) unsupported(key, ptr string) error {
	return &CompileError{Pointer: ptr, Unsupported: key, Msg: "unsupported keyword " + strconv.Quote(key)}
}

// unknown handles a keyword outside the subset: a compile error for
// internal schemas, a partial mark (unless pure annotation) for MCP.
func (c *compiler) unknown(key, ptr string) error {
	if c.mode == ModeInternal {
		return c.unsupported(key, ptr)
	}
	if !isAnnotation(key) {
		c.partial[key] = true
	}
	return nil
}

func (c *compiler) types(v any, ptr string) (typeMask, error) {
	list, ok := v.([]any)
	if !ok {
		list = []any{v}
	}
	var m typeMask
	for _, t := range list {
		name, isStr := t.(string)
		i := slices.Index(typeNames(), name)
		if !isStr || i < 0 {
			return 0, c.fail(ptr, "type must be one of "+strings.Join(typeNames(), ", "))
		}
		m |= 1 << i
	}
	if m == 0 {
		return 0, c.fail(ptr, "type must not be empty")
	}
	return m, nil
}

func (c *compiler) properties(n *node, v any, ptr string) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return c.fail(ptr, "properties must be an object")
	}
	for _, name := range slices.Sorted(maps.Keys(obj)) {
		s, err := c.node(obj[name], ptr+"/"+escapePointer(name))
		if err != nil {
			return err
		}
		p := n.addProp(name)
		p.schema, p.declared = s, true
	}
	return nil
}

// addProp returns the property entry for name, creating it if needed.
func (n *node) addProp(name string) *prop {
	if n.index == nil {
		n.index = map[string]int{}
	}
	i, ok := n.index[name]
	if !ok {
		i = len(n.props)
		n.index[name] = i
		n.props = append(n.props, prop{name: name})
	}
	return &n.props[i]
}

func (c *compiler) required(n *node, v any, ptr string) error {
	list, ok := v.([]any)
	if !ok {
		return c.fail(ptr, "required must be an array of strings")
	}
	seen := map[string]bool{}
	for _, e := range list {
		name, ok := e.(string)
		if !ok || seen[name] {
			return c.fail(ptr, "required must be an array of unique strings")
		}
		seen[name] = true
		n.addProp(name)
		n.required = append(n.required, n.index[name])
	}
	return nil
}

func (c *compiler) anyOf(n *node, v any, ptr string) error {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return c.fail(ptr, "anyOf must be a non-empty array")
	}
	if n.anyOf != nil {
		return c.fail(ptr, "anyOf and oneOf cannot be combined")
	}
	for i, sub := range list {
		s, err := c.node(sub, ptr+"/"+strconv.Itoa(i))
		if err != nil {
			return err
		}
		n.anyOf = append(n.anyOf, s)
	}
	return nil
}

// count reads a non-negative integer bound (2.0 is accepted as 2).
func (c *compiler) count(dst *int, v any, ptr string) error {
	s, isNum := v.(json.Number)
	f := parseNum(string(s))
	if !isNum || !(f >= 0 && f <= math.MaxInt32) || f != math.Trunc(f) {
		return c.fail(ptr, "must be a non-negative integer")
	}
	*dst = int(f)
	return nil
}

func (c *compiler) pattern(n *node, v any, ptr string) error {
	src, ok := v.(string)
	if !ok {
		return c.fail(ptr, "pattern must be a string")
	}
	re, err := regexp.Compile(src)
	switch {
	case err == nil:
		n.pattern = re
	case c.mode == ModeMCP:
		c.partial["pattern"] = true // not RE2 (e.g. lookahead): not enforced
	default:
		return c.fail(ptr, "pattern is not valid RE2: "+err.Error())
	}
	return nil
}

// escapePointer escapes one RFC 6901 reference token.
func escapePointer(tok string) string {
	if !strings.ContainsAny(tok, "~/") {
		return tok
	}
	return strings.ReplaceAll(strings.ReplaceAll(tok, "~", "~0"), "/", "~1")
}
