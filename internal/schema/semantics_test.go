package schema

import "testing"

// TestAnyOf: branches are tried without side effects; the node's own
// keywords still apply; syntax errors inside a branch are final.
func TestAnyOf(t *testing.T) {
	t.Parallel()
	s := mustCompile(t, `{
	 "anyOf": [
	  {"type":"object","properties":{"a":{"type":"object","properties":{"b":{"type":"integer"}},"additionalProperties":false}},"required":["a"]},
	  {"type":"object","properties":{"c":{"anyOf":[{"type":"string"},{"type":"null"}]}},"required":["c"]},
	  {"type":"array","items":{"type":"integer"}}
	 ],
	 "maxItems": 2
	}`, ModeMCP)
	cases := []validateCase{
		{"first branch", `{"a":{"b":1}}`, ""},
		{"second after deep failure", `{"a":{"b":1,"x":{"y":[1,{"z":2}]}},"c":null}`, ""},
		{"nested anyOf", `{"c":"s"}`, ""},
		{"third branch", `[1,2]`, ""},
		{"own keyword after branch", `[1,2,3]`, "(root): must contain at most 2 items"},
		{"no branch", `{"a":{"b":"x"}}`, "(root): value does not match any schema in anyOf"},
		{"no branch nested", `{"c":1}`, "(root): value does not match any schema in anyOf"},
		{"syntax inside branch", `{"a":{"b":1,"b":2}}`, `invalid JSON at byte 12: duplicate object key "b"`},
		{"syntax after branch", `[1,2`, "invalid JSON at byte 4: expected ',' or ']'"},
	}
	for _, c := range cases {
		err := s.Validate([]byte(c.doc))
		if (err == nil) != (c.want == "") || (err != nil && err.Error() != c.want) {
			t.Errorf("%s: Validate(%s) = %v, want %q", c.name, c.doc, err, c.want)
		}
	}
	oneOf := mustCompile(t, `{"oneOf":[{"type":"string"},{"type":"integer"}]}`, ModeMCP)
	if err := oneOf.Validate([]byte(`true`)); err == nil || err.Error() != "(root): value does not match any schema in anyOf" {
		t.Errorf("oneOf as anyOf: %v", err)
	}
}

// TestBooleanAndEmptySchemas: true/{} accept any valid JSON, false accepts
// nothing, all still require strict JSON.
func TestBooleanAndEmptySchemas(t *testing.T) {
	t.Parallel()
	anyOK := []string{`1`, `"s"`, `null`, `[]`, `{"a":[true,false]}`, `-1.5e-3`}
	for _, raw := range []string{`true`, `{}`} {
		s := mustCompile(t, raw, ModeInternal)
		for _, d := range anyOK {
			if err := s.Validate([]byte(d)); err != nil {
				t.Errorf("%s: Validate(%s) = %v", raw, d, err)
			}
		}
		if err := s.Validate([]byte(`[1,]`)); err == nil {
			t.Errorf("%s accepted invalid JSON", raw)
		}
	}
	never := mustCompile(t, `{"properties":{"x":false}}`, ModeInternal)
	if err := never.Validate([]byte(`{"x":1}`)); err == nil || err.Error() != "/x: no value is allowed here" {
		t.Errorf("false schema: %v", err)
	}
	enumEmpty := mustCompile(t, `{"enum":[]}`, ModeInternal)
	if err := enumEmpty.Validate([]byte(`1`)); err == nil || err.Error() != "(root): must be one of []" {
		t.Errorf("empty enum: %v", err)
	}
}

// TestEnumEquality: enum/const use JSON equality (numbers by value, objects
// unordered, no cross-type matches), together when both are present.
func TestEnumEquality(t *testing.T) {
	t.Parallel()
	s := mustCompile(t, `{"enum":[1,{"a":[1,"x"],"b":null},[false],"s"],"const":{"b":null,"a":[1.0,"x"]}}`, ModeInternal)
	if err := s.Validate([]byte(`{"b":null,"a":[1e0,"\u0078"]}`)); err != nil {
		t.Errorf("equal object rejected: %v", err)
	}
	for _, d := range []string{`1`, `[0]`, `"s"`, `{"a":[1,"x"]}`, `{"a":[1,"x"],"b":null,"c":1}`, `{"a":[1,"x","y"],"b":null}`, `{"a":[2,"x"],"b":null}`} {
		if s.Validate([]byte(d)) == nil {
			t.Errorf("Validate(%s) = nil, want enum/const failure", d)
		}
	}
	mixed := mustCompile(t, `{"enum":["a",1]}`, ModeInternal)
	if err := mixed.Validate([]byte(`"a"`)); err != nil {
		t.Errorf("mixed enum string: %v", err)
	}
	if err := mixed.Validate([]byte(`"b"`)); err == nil || err.Error() != `(root): must be one of ["a",1]` {
		t.Errorf("mixed enum miss: %v", err)
	}
}
