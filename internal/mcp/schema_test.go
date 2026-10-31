package mcp

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestNormalizeSchema(t *testing.T) {
	t.Parallel()
	got, err := NormalizeSchema(json.RawMessage(`{"properties":{"a":{"type":"string"}}}`))
	if err != nil {
		t.Fatalf("NormalizeSchema: %v", err)
	}
	want := `{"additionalProperties":false,"properties":{"a":{"type":"string"}},"type":"object"}`
	if string(got) != want {
		t.Fatalf("normalized = %s, want %s", got, want)
	}
	kept, err := NormalizeSchema(json.RawMessage(`{"type":"object","additionalProperties":true}`))
	if err != nil {
		t.Fatalf("NormalizeSchema: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(kept, &m); err != nil {
		t.Fatal(err)
	}
	if m["additionalProperties"] != true {
		t.Errorf("explicit additionalProperties lost: %s", kept)
	}
}

func TestNormalizeSchemaRejects(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		``,
		`[]`,
		`{"type":"string"}`,
		`{"type":["object","null"]}`,
		`{"type":"object","properties":[]}`,
		`{`,
	} {
		if _, err := NormalizeSchema(json.RawMessage(raw)); !errors.Is(err, ErrInvalidSchema) {
			t.Errorf("NormalizeSchema(%q) = %v, want ErrInvalidSchema", raw, err)
		}
	}
}
