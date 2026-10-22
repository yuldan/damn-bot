package schema

import (
	"errors"
	"testing"
)

// TestCompileBOM (EC-FIO-007): a schema file saved by a Windows editor with a
// UTF-8 BOM compiles, and the mark never repairs invalid JSON.
func TestCompileBOM(t *testing.T) {
	t.Parallel()
	const doc = `{"type":"string","minLength":1}`

	s := mustCompile(t, "\ufeff"+doc, ModeInternal)
	if err := s.Validate([]byte(`"a"`)); err != nil {
		t.Fatalf("conforming document rejected: %v", err)
	}
	if err := s.Validate([]byte(`""`)); err == nil {
		t.Fatal("minLength violation accepted after a BOM-prefixed schema")
	}

	// Negative control: the mark is not a repair. Invalid JSON still fails.
	if _, err := Compile([]byte("\ufeff{\"type\":"), ModeInternal); !errors.Is(err, ErrSchema) {
		t.Fatalf("invalid JSON after a BOM: err = %v, want ErrSchema", err)
	}
	// Negative control: only the prefix is a mark, so a BOM inside a const is
	// content and is compared literally.
	lit := mustCompile(t, `{"const":"\ufeffx"}`, ModeInternal)
	if err := lit.Validate([]byte(`"\ufeffx"`)); err != nil {
		t.Fatalf("interior U+FEFF in a const: %v", err)
	}
	if err := lit.Validate([]byte(`"x"`)); err == nil {
		t.Fatal("const without the interior U+FEFF accepted")
	}
	// Negative control: a document is wire data, not a file, so a document that
	// starts with a BOM is still a syntax error.
	if err := s.Validate([]byte("\ufeff\"a\"")); err == nil {
		t.Fatal("BOM-prefixed document accepted, want a syntax error")
	}
}
