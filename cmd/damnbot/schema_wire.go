package main

import (
	"fmt"
	"os"

	"github.com/damnbot/damnbot/internal/schema"
)

// compileAnswerSchema builds the SPEC §7.2 --answer-schema validator. The
// file is third-party input, so unsupported keywords are tolerated (ModeMCP)
// and an unreadable or invalid schema fails before the turn starts.
func compileAnswerSchema(path string) (func(string) error, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path) //nolint:gosec // G304: operator-supplied flag
	if err != nil {
		return nil, fmt.Errorf("answer schema: %w", err)
	}
	sc, err := schema.Compile(raw, schema.ModeMCP)
	if err != nil {
		return nil, fmt.Errorf("answer schema: %w", err)
	}
	return func(answer string) error { return sc.Validate([]byte(answer)) }, nil
}
