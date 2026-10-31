//go:build !minimal

package gemini

import (
	"encoding/json"
	"strings"
	"testing"
)

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return out
}

// TestSanitizeSubset: supported keywords survive, annotations are dropped,
// type arrays with null become nullable, and required is sorted/deduplicated.
func TestSanitizeSubset(t *testing.T) {
	t.Parallel()
	in := `{"$schema":"http://json-schema.org/draft-07/schema#","title":"t","type":"object",
		"properties":{"b":{"type":"integer","minimum":1},"a":{"type":["string","null"],"description":"d"},
		"c":{"type":"array","items":{"type":"string"},"minItems":1}},
		"required":["b","a","b"],"additionalProperties":false,"x-extra":1}`
	out, err := sanitize(json.RawMessage(in))
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	got := decode(t, out)
	if _, ok := got["$schema"]; ok {
		t.Fatalf("$schema survived: %s", out)
	}
	if _, ok := got["additionalProperties"]; ok {
		t.Fatalf("additionalProperties survived: %s", out)
	}
	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %v", got["properties"])
	}
	a, ok := props["a"].(map[string]any)
	if !ok || a["nullable"] != true || a["type"] != "string" {
		t.Fatalf("a = %v", props["a"])
	}
	req, ok := got["required"].([]any)
	if !ok || len(req) != 2 || req[0] != "a" || req[1] != "b" {
		t.Fatalf("required = %v", got["required"])
	}
}

// TestSanitizeDeterministic: equal schemas produce identical bytes, and the
// keyword order does not depend on map iteration.
func TestSanitizeDeterministic(t *testing.T) {
	t.Parallel()
	a := `{"type":"object","properties":{"b":{"type":"string"},"a":{"type":"integer"}}}`
	b := `{"properties":{"a":{"type":"integer"},"b":{"type":"string"}},"type":"object"}`
	first, err := sanitize(json.RawMessage(a))
	if err != nil {
		t.Fatalf("sanitize a: %v", err)
	}
	second, err := sanitize(json.RawMessage(b))
	if err != nil {
		t.Fatalf("sanitize b: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("not deterministic:\n%s\n%s", first, second)
	}
}

// TestSanitizeConstAndAnyOf: const lowers to a single-value enum; anyOf is a
// supported Gemini construct and is sanitized recursively.
func TestSanitizeConstAndAnyOf(t *testing.T) {
	t.Parallel()
	in := `{"type":"object","properties":{"k":{"const":"v"},"a":{"anyOf":[{"type":"string"},{"type":"integer"}]}}}`
	out, err := sanitize(json.RawMessage(in))
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	got := decode(t, out)
	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %v", got["properties"])
	}
	k, ok := props["k"].(map[string]any)
	if !ok {
		t.Fatalf("k = %v", props["k"])
	}
	enum, ok := k["enum"].([]any)
	if !ok || len(enum) != 1 || enum[0] != "v" {
		t.Fatalf("const enum = %v", k["enum"])
	}
	a, ok := props["a"].(map[string]any)
	if !ok {
		t.Fatalf("a = %v", props["a"])
	}
	if anyOf, ok := a["anyOf"].([]any); !ok || len(anyOf) != 2 {
		t.Fatalf("anyOf = %v", a["anyOf"])
	}
}

// TestSanitizeRejects: a keyword the Gemini subset cannot express is a clear
// error naming the location, never a silently dropped constraint.
func TestSanitizeRejects(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`{"type":"object","properties":{"a":{"$ref":"#/$defs/a"}}}`:          "$.properties.a.$ref",
		`{"type":"object","oneOf":[{"type":"object"}]}`:                      "$.oneOf",
		`{"type":"object","allOf":[{"type":"object"}]}`:                      "$.allOf",
		`{"type":"object","properties":{"a":{"type":["string","integer"]}}}`: "$.properties.a.type",
		`{"type":"object","properties":{"a":{"uniqueItems":true}}}`:          "$.properties.a.uniqueItems",
		`{"type":"object","properties":{"a":{"madeUp":1}}}`:                  "$.properties.a.madeUp",
		`{"type":"object","exclusiveMinimum":1}`:                             "$.exclusiveMinimum",
	}
	for in, want := range cases {
		out, err := sanitize(json.RawMessage(in))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("schema %s: out=%s err=%v, want error containing %q", in, out, err, want)
		}
	}
}

// TestSanitizeTopLevel: the parameters schema must be an object; boolean and
// array schemas are rejected rather than coerced.
func TestSanitizeTopLevel(t *testing.T) {
	t.Parallel()
	for _, in := range []string{`true`, `{"type":"string"}`} {
		if _, err := sanitize(json.RawMessage(in)); err == nil {
			t.Fatalf("schema %s accepted", in)
		}
	}
	out, err := sanitize(nil)
	if err != nil || string(out) != `{"type":"object"}` {
		t.Fatalf("empty schema = %s, %v", out, err)
	}
}
