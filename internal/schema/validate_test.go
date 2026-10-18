package schema

import (
	"errors"
	"strings"
	"testing"
)

// fixtureSchema exercises every keyword of the subset the way tool schemas
// use them (grep/todo shapes, SPEC §5).
const fixtureSchema = `{
 "type": "object",
 "description": "fixture",
 "properties": {
  "pattern": {"type": "string", "minLength": 1, "maxLength": 200, "description": "RE2"},
  "path": {"type": "string"},
  "case": {"type": "string", "enum": ["smart", "sensitive", "insensitive"]},
  "context": {"type": "integer", "minimum": 0, "maximum": 5},
  "mode": {"enum": ["matches", "files", "count"]},
  "limit": {"type": "integer", "minimum": 1, "maximum": 500, "default": 100},
  "ratio": {"type": "number", "minimum": 0.5, "maximum": 1.5},
  "name": {"type": "string", "pattern": "^[a-z_]+$"},
  "flag": {"type": "boolean"},
  "nothing": {"type": "null"},
  "version": {"const": 2},
  "items": {
   "type": "array", "minItems": 1, "maxItems": 3,
   "items": {
    "type": "object",
    "properties": {
     "id": {"type": "string"},
     "text": {"type": "string", "maxLength": 5},
     "status": {"enum": ["pending", "in_progress", "done"]}
    },
    "required": ["id", "text", "status"],
    "additionalProperties": false
   }
  },
  "meta": {"type": "object", "additionalProperties": {"type": "string"}},
  "free": {},
  "tags": {"type": ["string", "null"]},
  "a/b~c": {"type": "string"}
 },
 "required": ["pattern"],
 "additionalProperties": false
}`

// fixtureProps is the sorted property list of fixtureSchema as reported in
// additionalProperties messages.
const fixtureProps = "a/b~c, case, context, flag, free, items, limit, meta, mode, name, nothing, path, pattern, ratio, tags, version"

func validCases() []validateCase {
	return []validateCase{
		{"minimal", `{"pattern":"x"}`, ""},
		{"whitespace", " \t\r\n{ \"pattern\" : \"x\" } \n", ""},
		{"all fields", `{"pattern":"a.b","path":"src","case":"smart","context":5,"mode":"count","limit":500,"ratio":1.5,"name":"ab_c","flag":false,"nothing":null,"version":2.0,"items":[{"id":"1","text":"hi","status":"done"}],"meta":{"k":"v"},"free":{"x":[1,{"y":null}]},"tags":null,"a/b~c":"s"}`, ""},
		{"integer with zero fraction", `{"pattern":"x","context":1.0,"limit":1e2}`, ""},
		{"escaped enum value", `{"pattern":"x","case":"sm\u0061rt"}`, ""},
		{"escaped pattern value", `{"pattern":"x","name":"\u0061b"}`, ""},
		{"code points not bytes", `{"pattern":"x","items":[{"id":"1","text":"😀😀😀😀😀","status":"done"}]}`, ""},
		{"surrogate pair", `{"pattern":"\ud83d\ude00"}`, ""},
		{"required-only via escaped key", `{"p\u0061ttern":"x"}`, ""},
		{"tags string", `{"pattern":"x","tags":"t"}`, ""},
		{"negative zero", `{"pattern":"x","context":-0}`, ""},
	}
}

func violationCases() []validateCase {
	return []validateCase{
		{"root array", `[]`, "(root): expected object, got array"},
		{"root null", `null`, "(root): expected object, got null"},
		{"root string", `"x"`, "(root): expected object, got string"},
		{"missing required", `{}`, `(root): missing required property "pattern"`},
		{"missing required with others", `{"path":"a"}`, `(root): missing required property "pattern"`},
		{"empty string", `{"pattern":""}`, "/pattern: must be at least 1 characters long"},
		{"too long", `{"pattern":"` + strings.Repeat("a", 201) + `"}`, "/pattern: must be at most 200 characters long"},
		{"wrong type number", `{"pattern":1}`, "/pattern: expected string, got integer"},
		{"wrong type bool", `{"pattern":true}`, "/pattern: expected string, got boolean"},
		{"wrong type object", `{"pattern":{}}`, "/pattern: expected string, got object"},
		{"unknown property", `{"pattern":"x","patern":"y"}`, `/patern: unexpected property "patern" (allowed: ` + fixtureProps + `)`},
		{"enum miss", `{"pattern":"x","case":"Smart"}`, `/case: must be one of ["smart","sensitive","insensitive"]`},
		{"enum wrong type", `{"pattern":"x","case":1}`, "/case: expected string, got integer"},
		{"untyped enum miss", `{"pattern":"x","mode":"all"}`, `/mode: must be one of ["matches","files","count"]`},
		{"untyped enum number", `{"pattern":"x","mode":1}`, `/mode: must be one of ["matches","files","count"]`},
		{"untyped enum null", `{"pattern":"x","mode":null}`, `/mode: must be one of ["matches","files","count"]`},
		{"above maximum", `{"pattern":"x","context":6}`, "/context: must be <= 5"},
		{"below minimum", `{"pattern":"x","context":-1}`, "/context: must be >= 0"},
		{"fraction for integer", `{"pattern":"x","context":1.5}`, "/context: expected integer, got number"},
		{"string for integer", `{"pattern":"x","context":"1"}`, "/context: expected integer, got string"},
		{"overflow for integer", `{"pattern":"x","context":1e400}`, "/context: expected integer, got number"},
		{"limit zero", `{"pattern":"x","limit":0}`, "/limit: must be >= 1"},
		{"limit large", `{"pattern":"x","limit":501}`, "/limit: must be <= 500"},
		{"limit exponent", `{"pattern":"x","limit":5.01e2}`, "/limit: must be <= 500"},
		{"ratio low", `{"pattern":"x","ratio":0.4}`, "/ratio: must be >= 0.5"},
		{"ratio high", `{"pattern":"x","ratio":1.6}`, "/ratio: must be <= 1.5"},
		{"ratio string", `{"pattern":"x","ratio":"1"}`, "/ratio: expected number, got string"},
		{"pattern mismatch", `{"pattern":"x","name":"Abc"}`, `/name: must match pattern "^[a-z_]+$"`},
		{"pattern mismatch escaped", `{"pattern":"x","name":"\u0041"}`, `/name: must match pattern "^[a-z_]+$"`},
		{"bool as string", `{"pattern":"x","flag":"true"}`, "/flag: expected boolean, got string"},
		{"bool null", `{"pattern":"x","flag":null}`, "/flag: expected boolean, got null"},
		{"null false", `{"pattern":"x","nothing":false}`, "/nothing: expected null, got boolean"},
		{"const miss", `{"pattern":"x","version":3}`, "/version: must be 2"},
		{"const type", `{"pattern":"x","version":"2"}`, "/version: must be 2"},
		{"items empty", `{"pattern":"x","items":[]}`, "/items: must contain at least 1 items"},
		{"items too many", `{"pattern":"x","items":[` + strings.Repeat(`{"id":"1","text":"t","status":"done"},`, 3) + `{"id":"1","text":"t","status":"done"}]}`, "/items: must contain at most 3 items"},
		{"items not array", `{"pattern":"x","items":{}}`, "/items: expected array, got object"},
		{"item not object", `{"pattern":"x","items":["a"]}`, "/items/0: expected object, got string"},
		{"item missing id", `{"pattern":"x","items":[{"text":"t","status":"done"}]}`, `/items/0: missing required property "id"`},
		{"item missing status", `{"pattern":"x","items":[{"id":"1","text":"t"}]}`, `/items/0: missing required property "status"`},
		{"item text long", `{"pattern":"x","items":[{"id":"1","text":"abcdef","status":"done"}]}`, "/items/0/text: must be at most 5 characters long"},
		{"item text long code points", `{"pattern":"x","items":[{"id":"1","text":"😀😀😀😀😀😀","status":"done"}]}`, "/items/0/text: must be at most 5 characters long"},
		{"second item status", `{"pattern":"x","items":[{"id":"1","text":"t","status":"done"},{"id":"2","text":"t","status":"later"}]}`, `/items/1/status: must be one of ["pending","in_progress","done"]`},
		{"item extra", `{"pattern":"x","items":[{"id":"1","text":"t","status":"done","x":1}]}`, `/items/0/x: unexpected property "x" (allowed: id, status, text)`},
		{"meta value", `{"pattern":"x","meta":{"k":1}}`, "/meta/k: expected string, got integer"},
		{"meta type", `{"pattern":"x","meta":[]}`, "/meta: expected object, got array"},
		{"tags number", `{"pattern":"x","tags":1}`, "/tags: expected null or string, got integer"},
		{"escaped unknown key", `{"pattern":"x","\u00e9":1}`, `/é: unexpected property "é" (allowed: ` + fixtureProps + `)`},
		{"pointer escaping", `{"pattern":"x","a/b~c":1}`, "/a~1b~0c: expected string, got integer"},
		{"pointer escaping meta key", `{"pattern":"x","meta":{"x/y":true}}`, "/meta/x~1y: expected string, got boolean"},
	}
}

// TestValidate pins every model-facing message (E-68: the expected strings
// are the golden) and the error contract: errors.Is(err, ErrInvalid).
func TestValidate(t *testing.T) {
	t.Parallel()
	s := mustCompile(t, fixtureSchema, ModeInternal)
	invalid := append(syntaxCases(), violationCases()...)
	if len(invalid) < 60 {
		t.Fatalf("%d invalid cases, want ≥ 60 (SPEC P3)", len(invalid))
	}
	for _, c := range append(validCases(), invalid...) {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := s.Validate([]byte(c.doc))
			if c.want == "" {
				if err != nil {
					t.Fatalf("Validate(%q) = %v, want nil", c.doc, err)
				}
				return
			}
			if err == nil || err.Error() != c.want {
				t.Fatalf("Validate(%q) = %v\nwant %s", c.doc, err, c.want)
			}
			var ve *Error
			if !errors.Is(err, ErrInvalid) || !errors.As(err, &ve) {
				t.Fatalf("error %T does not satisfy the ErrInvalid/*Error contract", err)
			}
		})
	}
}

// TestValidateNoDuplicateKeys: the schema-aware walk enforces SPEC §4.4
// like check(), on declared properties (bitset) and other members alike.
func TestValidateNoDuplicateKeys(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{ModeInternal, ModeMCP} {
		for _, raw := range []string{fixtureSchema, `true`, `{}`} {
			s := mustCompile(t, raw, mode)
			for _, d := range duplicateDocs() {
				var ve *Error
				if err := s.Validate([]byte(d)); !errors.As(err, &ve) || ve.Kind != KindSyntax {
					t.Errorf("schema %.20q: Validate(%.60q) = %v, want duplicate-key syntax error", raw, d, err)
				}
			}
		}
	}
}

// TestValidateConcurrent: one compiled schema serves concurrent callers
// (pooled scratch must not leak between calls); run under -race.
func TestValidateConcurrent(t *testing.T) {
	t.Parallel()
	s := mustCompile(t, fixtureSchema, ModeInternal)
	for _, c := range append(validCases(), violationCases()...) {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			for range 20 {
				err := s.Validate([]byte(c.doc))
				if (err == nil) != (c.want == "") || (err != nil && err.Error() != c.want) {
					t.Fatalf("Validate(%q) = %v, want %q", c.doc, err, c.want)
				}
			}
		})
	}
}

// TestValidateDoesNotRetainInput: the caller may reuse its buffer at once.
func TestValidateDoesNotRetainInput(t *testing.T) {
	t.Parallel()
	s := mustCompile(t, fixtureSchema, ModeInternal)
	buf := []byte(`{"pattern":"x","zz":1}`)
	err := s.Validate(buf)
	copy(buf, `{"PATTERN":"y","ZZ":2}`)
	if err == nil || err.Error() != `/zz: unexpected property "zz" (allowed: `+fixtureProps+`)` {
		t.Fatalf("error changed with caller buffer: %v", err)
	}
}
