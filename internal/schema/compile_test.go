package schema

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func mustCompile(t testing.TB, raw string, mode Mode) *Schema {
	t.Helper()
	s, err := Compile([]byte(raw), mode)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return s
}

// TestSchemaKeywordsSupported (SPEC §4.4, ADR-008): internal schemas accept
// exactly the subset; any other keyword, and anyOf/oneOf, fail to compile
// with CompileError.Unsupported naming it.
func TestSchemaKeywordsSupported(t *testing.T) {
	t.Parallel()
	supported := []string{
		`{"type":"string"}`, `{"type":["integer","null"]}`, `{"properties":{"a":{}}}`, `{"required":["a"]}`,
		`{"additionalProperties":false}`, `{"additionalProperties":{"type":"string"}}`, `{"enum":[1,"a",null]}`,
		`{"const":{"a":[1]}}`, `{"items":{"type":"string"}}`, `{"minimum":-1.5}`, `{"maximum":10}`,
		`{"minLength":0}`, `{"maxLength":2.0}`, `{"minItems":1}`, `{"maxItems":3}`, `{"pattern":"^a+$"}`,
		`{"description":"d"}`, `{"default":{"any":"thing"}}`, `true`, `false`,
	}
	for _, raw := range supported {
		if _, err := Compile([]byte(raw), ModeInternal); err != nil {
			t.Errorf("Compile(%s) = %v, want nil", raw, err)
		}
	}
	unsupported := map[string]string{
		"anyOf": `{"anyOf":[{}]}`, "oneOf": `{"oneOf":[{}]}`, "allOf": `{"allOf":[{}]}`, "not": `{"not":{}}`,
		"$ref": `{"$ref":"#"}`, "$defs": `{"$defs":{}}`, "format": `{"format":"uri"}`, "title": `{"title":"t"}`,
		"$schema": `{"$schema":"x"}`, "uniqueItems": `{"uniqueItems":true}`, "multipleOf": `{"multipleOf":2}`,
		"exclusiveMinimum": `{"exclusiveMinimum":0}`, "patternProperties": `{"patternProperties":{}}`,
		"items": `{"items":[{}]}`, "prefixItems": `{"prefixItems":[]}`, "nullable": `{"properties":{"a":{"nullable":true}}}`,
	}
	for kw, raw := range unsupported {
		var se *CompileError
		if _, err := Compile([]byte(raw), ModeInternal); !errors.As(err, &se) || se.Unsupported != kw || !errors.Is(err, ErrSchema) {
			t.Errorf("Compile(%s) = %v, want unsupported %q", raw, err, kw)
		}
	}
}

// TestCompileInvalidSchema: malformed values of known keywords are errors in
// both modes (an MCP tool with such a schema is not registered).
func TestCompileInvalidSchema(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`{"type":"any"}`:                    `schema /type: type must be one of null, boolean, object, array, number, string, integer`,
		`{"type":[]}`:                       `schema /type: type must not be empty`,
		`{"type":1}`:                        `schema /type: type must be one of null, boolean, object, array, number, string, integer`,
		`{"properties":[]}`:                 `schema /properties: properties must be an object`,
		`{"properties":{"a":1}}`:            `schema /properties/a: schema must be an object or boolean`,
		`{"properties":{"a/b":"x"}}`:        `schema /properties/a~1b: schema must be an object or boolean`,
		`{"required":"a"}`:                  `schema /required: required must be an array of strings`,
		`{"required":["a","a"]}`:            `schema /required: required must be an array of unique strings`,
		`{"required":[1]}`:                  `schema /required: required must be an array of unique strings`,
		`{"minLength":-1}`:                  `schema /minLength: must be a non-negative integer`,
		`{"maxItems":1.5}`:                  `schema /maxItems: must be a non-negative integer`,
		`{"minItems":"1"}`:                  `schema /minItems: must be a non-negative integer`,
		`{"minimum":"0"}`:                   `schema /minimum: minimum must be a number`,
		`{"maximum":null}`:                  `schema /maximum: maximum must be a number`,
		`{"enum":{}}`:                       `schema /enum: enum must be an array`,
		`{"pattern":1}`:                     `schema /pattern: pattern must be a string`,
		`{"description":1}`:                 `schema /description: description must be a string`,
		`{"additionalProperties":1}`:        `schema /additionalProperties: schema must be an object or boolean`,
		`"object"`:                          `schema (root): schema must be an object or boolean`,
		`{"type":"object",}`:                `schema (root): invalid JSON at byte 17: expected object key`,
		`{"items":` + deep(MaxDepth) + `}`:  `schema (root): invalid JSON at byte 72: nesting deeper than 64`,
		`{"type":"object","type":"string"}`: `schema (root): invalid JSON at byte 17: duplicate object key "type"`,
	}
	for raw, want := range cases {
		for _, mode := range []Mode{ModeInternal, ModeMCP} {
			_, err := Compile([]byte(raw), mode)
			if err == nil || err.Error() != want || !errors.Is(err, ErrSchema) {
				t.Errorf("mode %d: Compile(%.40s) = %v\nwant %s", mode, raw, err, want)
			}
		}
	}
	_, err := Compile([]byte(`{"pattern":"(?=a)"}`), ModeInternal) // ModeMCP: partial (TestCompileMCPPartial)
	if err == nil || err.Error() != "schema /pattern: pattern is not valid RE2: error parsing regexp: invalid or unsupported Perl syntax: `(?=`" {
		t.Errorf("non-RE2 pattern: %v", err)
	}
	// A schema nesting limit is implied: each subschema is one JSON level.
	if _, err := Compile([]byte(strings.Repeat(`{"items":`, MaxDepth)+`{}`+strings.Repeat(`}`, MaxDepth)), ModeInternal); err == nil {
		t.Error("schema deeper than MaxDepth accepted")
	}
	if _, err := Compile([]byte(strings.Repeat(`{"items":`, MaxDepth-1)+`{}`+strings.Repeat(`}`, MaxDepth-1)), ModeInternal); err != nil {
		t.Errorf("schema at MaxDepth rejected: %v", err)
	}
}

// deep returns n nested arrays.
func deep(n int) string { return strings.Repeat("[", n) + strings.Repeat("]", n) }

// TestCompileShape pins the compiled form of the keywords the validator
// relies on (type sets, required-only entries, bounds, false schema).
func TestCompileShape(t *testing.T) {
	t.Parallel()
	s := mustCompile(t, `{"type":["string","null"],"properties":{"a":{"type":"integer","minimum":1,"maxLength":3},"b":false},"required":["a","c"],"additionalProperties":false}`, ModeInternal)
	r := s.root
	switch {
	case r.types != tString|tNull, !r.noAddl, r.addl != nil:
		t.Errorf("root: types %b noAddl %v addl %v", r.types, r.noAddl, r.addl)
	case len(r.props) != 3 || !r.props[r.index["a"]].declared || r.props[r.index["c"]].declared:
		t.Errorf("props %+v", r.props)
	case !slices.Equal(r.required, []int{r.index["a"], r.index["c"]}):
		t.Errorf("required %v", r.required)
	}
	a, b := r.props[r.index["a"]].schema, r.props[r.index["b"]].schema
	if a.types != tInteger || !a.hasMin || a.min != 1 || a.counts != [4]int{-1, 3, -1, -1} || !b.never {
		t.Errorf("a = %+v, b = %+v", a, b)
	}
	all := mustCompile(t, `{"type":["null","boolean","object","array","number","string","integer"]}`, ModeInternal)
	if all.root.types != tNull|tBool|tObject|tArray|tNumber|tString|tInteger {
		t.Errorf("all types = %b", all.root.types)
	}
}

// TestCompileMCPPartial (SPEC §4.4, ADR-095): MCP schemas compile with
// unsupported keywords ignored and listed by Partial; annotations are not
// partial; oneOf is enforced as anyOf.
func TestCompileMCPPartial(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		want []string
	}{
		{`{"type":"object","title":"t","$schema":"x","$id":"i","$comment":"c","examples":[1],"deprecated":false,"readOnly":true,"writeOnly":false}`, nil},
		{`{"type":"string","format":"uri","properties":{"a":{"$ref":"#/$defs/x"}},"$defs":{"x":{}}}`, []string{"$defs", "$ref", "format"}},
		{`{"items":[{"type":"string"}],"uniqueItems":true}`, []string{"items", "uniqueItems"}},
		{`{"pattern":"(?=a)b"}`, []string{"pattern"}},
		{`{"oneOf":[{"type":"string"},{"type":"integer"}]}`, []string{"oneOf"}},
		{`{"anyOf":[{"type":"string"}]}`, nil},
	}
	for _, c := range cases {
		s := mustCompile(t, c.raw, ModeMCP)
		if got := s.Partial(); !slices.Equal(got, c.want) {
			t.Errorf("Partial(%s) = %v, want %v", c.raw, got, c.want)
		}
	}
	s := mustCompile(t, `{"oneOf":[{"type":"string"},{"type":"integer"}]}`, ModeMCP)
	s.Partial()[0] = "mutated"
	if s.Partial()[0] != "oneOf" {
		t.Error("Partial exposes internal slice")
	}
	if _, err := Compile([]byte(`{"anyOf":[]}`), ModeMCP); err == nil {
		t.Error("empty anyOf accepted")
	}
	if _, err := Compile([]byte(`{"anyOf":[{}],"oneOf":[{}]}`), ModeMCP); err == nil {
		t.Error("anyOf+oneOf accepted")
	}
}
