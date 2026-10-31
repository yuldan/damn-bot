//go:build !minimal

package gemini

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// unsupported reports a JSON Schema keyword the native API rejects and that
// cannot be lowered without changing its meaning.
func unsupported(k string) bool {
	switch k {
	case "$ref", "$defs", "definitions", "oneOf", "allOf", "not",
		"patternProperties", "if", "then", "else", "multipleOf", "uniqueItems",
		"exclusiveMinimum", "exclusiveMaximum", "minProperties", "maxProperties",
		"propertyNames", "contains", "prefixItems", "unevaluatedItems",
		"unevaluatedProperties", "dependentRequired", "dependentSchemas":
		return true
	default:
		return false
	}
}

// ignorable reports a JSON Schema annotation with no effect on validation;
// the native API rejects the object if it is sent, so it is dropped.
func ignorable(k string) bool {
	switch k {
	case "$schema", "$id", "$comment", "examples", "readOnly", "writeOnly",
		"deprecated", "contentEncoding", "contentMediaType", "$anchor",
		"$dynamicRef", "$dynamicAnchor":
		return true
	default:
		return false
	}
}

// validFormat reports a format value the native Schema proto understands; any
// other value is dropped rather than forwarded (it is only an annotation).
func validFormat(f string) bool {
	switch f {
	case "enum", "date-time", "int32", "int64", "float", "double", "byte":
		return true
	default:
		return false
	}
}

func validType(t string) bool {
	switch t {
	case "object", "array", "string", "number", "integer", "boolean":
		return true
	default:
		return false
	}
}

// sanitize lowers a tool parameters JSON Schema to the subset the Gemini
// native API accepts. The result is deterministic (sorted object keys) and
// either valid for the API or an error naming the unsupported location; a
// keyword is never silently forwarded.
func sanitize(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{"type":"object"}`), nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("parameters must be a JSON Schema object: %w", err)
	}
	out, err := sanitizeObject(obj, "$")
	if err != nil {
		return nil, err
	}
	var typ string
	if t, ok := out["type"]; !ok || json.Unmarshal(t, &typ) != nil || typ != "object" {
		return nil, fmt.Errorf("parameters: top-level type must be object")
	}
	return marshal(out)
}

// sanitizeObject cleans one schema level. Keys are visited in sorted order so
// the first error and the resulting bytes do not depend on map iteration.
func sanitizeObject(in map[string]json.RawMessage, path string) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(in))
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := in[k]
		at := path + "." + k
		if err := sanitizeKey(out, k, v, at); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func sanitizeKey(out map[string]json.RawMessage, k string, v json.RawMessage, at string) error {
	switch {
	case k == "type":
		return schemaType(out, v, at)
	case k == "const":
		out["enum"] = append(append([]byte{'['}, v...), ']')
		return nil
	case k == "additionalProperties":
		// The native Schema has no additionalProperties: dropping it matches
		// both true and the common false (objects are closed server-side).
		var b bool
		var m map[string]json.RawMessage
		if json.Unmarshal(v, &b) != nil && json.Unmarshal(v, &m) != nil {
			return fmt.Errorf("%s: additionalProperties must be a boolean or an object", at)
		}
		return nil
	case unsupported(k):
		return fmt.Errorf("%s: keyword %q is not supported by the Gemini schema subset", at, k)
	case ignorable(k), strings.HasPrefix(k, "x-"):
		return nil
	}
	if ok, err := sanitizeShape(out, k, v, at); ok {
		return err
	}
	if sanitizeAnnotation(out, k, v) {
		return nil
	}
	return fmt.Errorf("%s: unknown JSON Schema keyword %q; the Gemini subset must be expressed with supported keywords", at, k)
}

// sanitizeShape handles keywords whose value is a schema, a list of schemas,
// or a list of names. ok is false when k is not one of them.
func sanitizeShape(out map[string]json.RawMessage, k string, v json.RawMessage, at string) (bool, error) {
	switch k {
	case "nullable":
		var b bool
		if err := json.Unmarshal(v, &b); err != nil {
			return true, fmt.Errorf("%s: nullable must be a boolean", at)
		}
		if b {
			out["nullable"] = json.RawMessage("true")
		}
	case "properties":
		return true, objectMap(out, "properties", v, at)
	case "items":
		child, err := childSchema(v, at)
		if err != nil {
			return true, err
		}
		out["items"] = child
	case "anyOf":
		raw, err := schemaArray(v, at)
		if err != nil {
			return true, err
		}
		out["anyOf"] = raw
	case "required", "propertyOrdering":
		return true, stringArray(out, k, v, at)
	case "enum":
		var arr []json.RawMessage
		if err := json.Unmarshal(v, &arr); err != nil || len(arr) == 0 {
			return true, fmt.Errorf("%s: enum must be a non-empty array", at)
		}
		out["enum"] = v
	case "format":
		return true, format(out, v, at)
	default:
		return false, nil
	}
	return true, nil
}

// sanitizeAnnotation keeps scalar keywords verbatim; the API validates their
// values, and none of them changes the schema's structure.
func sanitizeAnnotation(out map[string]json.RawMessage, k string, v json.RawMessage) bool {
	switch k {
	case "description", "title", "pattern", "minimum", "maximum", "minLength",
		"maxLength", "minItems", "maxItems", "default", "example":
		out[k] = v
		return true
	default:
		return false
	}
}

func schemaType(out map[string]json.RawMessage, v json.RawMessage, at string) error {
	var one string
	if json.Unmarshal(v, &one) == nil {
		if !validType(one) {
			return fmt.Errorf("%s: type %q is not a Gemini schema type", at, one)
		}
		out["type"] = v
		return nil
	}
	var many []string
	if json.Unmarshal(v, &many) != nil || len(many) == 0 {
		return fmt.Errorf("%s: type must be a string or an array of strings", at)
	}
	var null bool
	var nonNull []string
	for _, t := range many {
		if t == "null" {
			null = true
			continue
		}
		if !validType(t) {
			return fmt.Errorf("%s: type %q is not a Gemini schema type", at, t)
		}
		nonNull = append(nonNull, t)
	}
	if len(nonNull) != 1 {
		return fmt.Errorf("%s: multiple types %v are not supported; use one type plus nullable", at, many)
	}
	raw, err := marshal(nonNull[0])
	if err != nil {
		return err
	}
	out["type"] = raw
	if null {
		out["nullable"] = json.RawMessage("true")
	}
	return nil
}

func objectMap(out map[string]json.RawMessage, k string, v json.RawMessage, at string) error {
	var props map[string]json.RawMessage
	if err := json.Unmarshal(v, &props); err != nil {
		return fmt.Errorf("%s: must be an object of schemas", at)
	}
	cleaned := make(map[string]json.RawMessage, len(props))
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		child, err := sanitizeObject2(props[name], at+"."+name)
		if err != nil {
			return err
		}
		raw, merr := marshal(child)
		if merr != nil {
			return merr
		}
		cleaned[name] = raw
	}
	raw, err := marshal(cleaned)
	if err != nil {
		return err
	}
	out[k] = raw
	return nil
}

// sanitizeObject2 unwraps a raw child schema before sanitizing it.
func sanitizeObject2(raw json.RawMessage, at string) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("%s: must be a schema object", at)
	}
	return sanitizeObject(obj, at)
}

func childSchema(raw json.RawMessage, at string) (json.RawMessage, error) {
	obj, err := sanitizeObject2(raw, at)
	if err != nil {
		return nil, err
	}
	return marshal(obj)
}

func schemaArray(raw json.RawMessage, at string) (json.RawMessage, error) {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
		return nil, fmt.Errorf("%s: must be a non-empty array of schemas", at)
	}
	out := make([]json.RawMessage, len(arr))
	for i, item := range arr {
		child, err := childSchema(item, fmt.Sprintf("%s[%d]", at, i))
		if err != nil {
			return nil, err
		}
		out[i] = child
	}
	return marshal(out)
}

func stringArray(out map[string]json.RawMessage, k string, v json.RawMessage, at string) error {
	var arr []string
	if err := json.Unmarshal(v, &arr); err != nil {
		return fmt.Errorf("%s: must be an array of strings", at)
	}
	slices.Sort(arr)
	arr = slices.Compact(arr)
	if len(arr) == 0 {
		return nil
	}
	raw, err := marshal(arr)
	if err != nil {
		return err
	}
	out[k] = raw
	return nil
}

func format(out map[string]json.RawMessage, v json.RawMessage, at string) error {
	var f string
	if json.Unmarshal(v, &f) != nil {
		return fmt.Errorf("%s: format must be a string", at)
	}
	if validFormat(f) {
		out["format"] = v
	}
	return nil
}
