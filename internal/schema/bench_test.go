package schema

import (
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/timing"
)

// editSchema mirrors an edit-style tool schema (path + two text blocks).
const editSchema = `{
 "type": "object",
 "properties": {
  "path": {"type": "string", "minLength": 1, "maxLength": 4096},
  "old_string": {"type": "string", "maxLength": 100000},
  "new_string": {"type": "string", "maxLength": 100000},
  "replace_all": {"type": "boolean"},
  "anchor": {"type": "object", "properties": {"line": {"type": "integer", "minimum": 1}, "hash": {"type": "string", "pattern": "^[0-9a-f]{4}$"}}, "required": ["line"], "additionalProperties": false}
 },
 "required": ["path", "old_string", "new_string"],
 "additionalProperties": false
}`

// args1K is ≈ 1 KiB of tool arguments with escapes (E-37 workload).
func args1K() []byte {
	body := strings.Repeat(`func f() {\n\treturn \"x\"\n}\n`, 16)
	return []byte(`{"path":"internal/schema/validate.go","old_string":"` + body +
		`","new_string":"` + body + `// \u00e9\n","replace_all":false,"anchor":{"line":42,"hash":"a1b2"}}`)
}

// TestValidateAllocs locks the E-37 success path: 0 allocs/op for 1 KiB of
// arguments, and the ≤ 20 µs latency budget through the timing gate.
func TestValidateAllocs(t *testing.T) {
	s := mustCompile(t, editSchema, ModeInternal)
	doc := args1K()
	if err := s.Validate(doc); err != nil {
		t.Fatal(err)
	}
	// -race makes sync.Pool drop items at random, so the scratch state is
	// sometimes rebuilt; the E-37 ceiling (2) still applies there.
	limit := 0.0
	if timing.RaceEnabled {
		limit = 2
	}
	const runs = 1000
	start := time.Now()
	allocs := testing.AllocsPerRun(runs, func() {
		if err := s.Validate(doc); err != nil {
			t.Fatal(err)
		}
	})
	perOp := time.Since(start) / (runs + 1) // AllocsPerRun adds one warm-up call
	if allocs > limit {
		t.Fatalf("Validate 1 KiB: %.2f allocs/op, want ≤ %v (E-37)", allocs, limit)
	}
	timing.Check(t, "schema Validate 1 KiB (E-37)", perOp, 20*time.Microsecond)
}

// BenchmarkValidate: success path of validating 1 KiB of arguments
// (ENGINEERING E-37 budget for lookup + validation: ≤ 2 allocs/op, ≤ 20 µs).
func BenchmarkValidate(b *testing.B) {
	s := mustCompile(b, editSchema, ModeInternal)
	doc := args1K()
	if err := s.Validate(doc); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(doc)))
	b.ReportAllocs()
	for b.Loop() {
		if err := s.Validate(doc); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkValidateInvalid: failure path (error construction, cold).
func BenchmarkValidateInvalid(b *testing.B) {
	s := mustCompile(b, editSchema, ModeInternal)
	doc := []byte(`{"path":"a","old_string":"x","new_string":"y","anchor":{"line":0}}`)
	b.ReportAllocs()
	for b.Loop() {
		if s.Validate(doc) == nil {
			b.Fatal("accepted")
		}
	}
}

// BenchmarkCompile: one-time cost per tool schema at registry build.
func BenchmarkCompile(b *testing.B) {
	raw := []byte(fixtureSchema)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Compile(raw, ModeInternal); err != nil {
			b.Fatal(err)
		}
	}
}
