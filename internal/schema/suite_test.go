package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"testing"
)

// suiteMinExecuted is the number of official test cases that must run; a
// drop means a keyword lost coverage (skips are only for keywords outside the
// subset). Update together with the vendored suite (testdata/jsts/ORIGIN.md).
const suiteMinExecuted = 344

type suiteGroup struct {
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
	Tests       []struct {
		Description string          `json:"description"`
		Data        json.RawMessage `json:"data"`
		Valid       bool            `json:"valid"`
	} `json:"tests"`
}

// TestJSONSchemaTestSuite runs the vendored JSON-Schema-Test-Suite subset
// (ADR-008): every group whose schema stays inside the subset must agree with
// the official expectation on every case. The files' byte identity against
// testdata/jsts/ORIGIN.md is checked by archtest TestAttributionHeaders
// (ADR-099).
func TestJSONSchemaTestSuite(t *testing.T) {
	t.Parallel()
	dir := os.DirFS("testdata/jsts")
	names, err := fs.Glob(dir, "*.json")
	if err != nil || len(names) == 0 {
		t.Fatalf("suite files: %v, %v", names, err)
	}
	executed, skipped := 0, 0
	for _, name := range names {
		raw, err := fs.ReadFile(dir, name)
		if err != nil {
			t.Fatal(err)
		}
		e, s := runSuiteFile(t, name, raw)
		executed, skipped = executed+e, skipped+s
	}
	t.Logf("JSON-Schema-Test-Suite: %d cases executed, %d skipped (keywords outside subset)", executed, skipped)
	if executed < suiteMinExecuted {
		t.Errorf("executed %d official cases, want ≥ %d", executed, suiteMinExecuted)
	}
}

// runSuiteFile checks every case of one suite file and returns the executed
// and skipped case counts.
func runSuiteFile(t *testing.T, name string, raw []byte) (executed, skipped int) {
	t.Helper()
	var groups []suiteGroup
	if err := json.Unmarshal(raw, &groups); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	mode := ModeInternal
	if name == "anyOf.json" {
		mode = ModeMCP // anyOf is admitted only for MCP schemas (SPEC §4.4)
	}
	for _, g := range groups {
		s, ok := compileSuiteSchema(t, name, g, mode)
		if !ok {
			skipped += len(g.Tests)
			continue
		}
		for _, c := range g.Tests {
			executed++
			if err := s.Validate(c.Data); (err == nil) != c.Valid {
				t.Errorf("%s / %s / %s: valid=%v, got err=%v", name, g.Description, c.Description, c.Valid, err)
			}
		}
	}
	return executed, skipped
}

// compileSuiteSchema compiles a group schema; it reports false (skip) only
// when the schema uses a keyword outside the subset.
func compileSuiteSchema(t *testing.T, file string, g suiteGroup, mode Mode) (*Schema, bool) {
	t.Helper()
	s, err := Compile(stripMeta(t, g.Schema), mode)
	var se *CompileError
	switch {
	case err == nil && len(s.Partial()) == 0:
		return s, true
	case err == nil:
		return nil, false // MCP mode ignored a keyword: expectations do not apply
	case errors.As(err, &se) && se.Unsupported != "":
		return nil, false
	default:
		t.Errorf("%s / %s: compile: %v", file, g.Description, err)
		return nil, false
	}
}

// stripMeta drops the root "$schema"/"$comment" annotations, which internal
// schemas never carry, re-encoding numbers exactly.
func stripMeta(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return raw
	}
	delete(m, "$schema")
	delete(m, "$comment")
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
