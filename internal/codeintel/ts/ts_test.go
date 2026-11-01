package ts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

const cSource = `#include <stdio.h>
struct point { int x; int y; };
enum color { RED };
int add(int a, int b) { return a + b; }
`

func TestParseCExtractsDefinitions(t *testing.T) {
	t.Parallel()
	syms, err := Parse(t.Context(), C, []byte(cSource), 0, 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := map[string]string{}
	for _, s := range syms {
		got[s.Name] = s.Kind
	}
	for name, kind := range map[string]string{"point": "struct", "color": "enum", "add": "func"} {
		if got[name] != kind {
			t.Fatalf("symbols = %+v (want %s=%s)", syms, name, kind)
		}
	}
}

func TestParseCppAndUnsupported(t *testing.T) {
	t.Parallel()
	if _, err := Parse(t.Context(), Cpp, []byte("class Widget { public: void run(); };\nnamespace ns { int f(); }\n"), 0, 0); err != nil {
		t.Fatalf("cpp: %v", err)
	}
	if _, err := Parse(t.Context(), "rust", []byte("fn main() {}"), 0, 0); !errors.Is(err, ErrUnsupportedLanguage) {
		t.Fatalf("unsupported = %v", err)
	}
}

func TestParseCorruptAndOversize(t *testing.T) {
	t.Parallel()
	inputs := []string{"", "\x00\x01\x02", "int f( {", strings.Repeat("{", 5000), "struct"}
	for _, in := range inputs {
		if _, err := Parse(t.Context(), C, []byte(in), 0, 0); err != nil && !errors.Is(err, ErrParse) {
			t.Fatalf("corrupt %q: %v", in, err)
		}
	}
	if _, err := Parse(t.Context(), C, []byte(strings.Repeat("x", MaxSourceBytes+1)), 0, 0); !errors.Is(err, ErrParse) {
		t.Fatalf("oversize = %v", err)
	}
}

// TestMemoryLimitRejects proves the cap is enforced by the runtime, not a
// claim: one wasm page (64 KiB) cannot host the parser.
func TestMemoryLimitRejects(t *testing.T) {
	t.Parallel()
	_, err := Parse(t.Context(), C, []byte(cSource), 1, 0)
	if !errors.Is(err, ErrParse) {
		t.Fatalf("memory limit = %v", err)
	}
}

func TestTimeoutAndParallelDeterminism(t *testing.T) {
	t.Parallel()
	deep := []byte(strings.Repeat("int f(){", 2000) + strings.Repeat("}", 2000))
	if _, err := Parse(t.Context(), C, deep, 0, 1); err == nil {
		t.Fatal("1ms timeout accepted deep input")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Parse(ctx, C, []byte(cSource), 0, 0); err == nil {
		t.Fatal("canceled context accepted")
	}
	var wg sync.WaitGroup
	results := make([][]Symbol, 8)
	errs := make([]error, 8)
	for i := range results {
		wg.Go(func() { results[i], errs[i] = Parse(t.Context(), C, []byte(cSource), 0, 0) })
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil || len(results[i]) != len(results[0]) {
			t.Fatalf("parallel %d: %v vs %v", i, errs[i], results[0])
		}
	}
}
