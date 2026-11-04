package ts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
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

func symbolKinds(t *testing.T, lang Language, src string) map[string]string {
	t.Helper()
	syms, err := Parse(t.Context(), lang, []byte(src), 0, 0)
	if err != nil {
		t.Fatalf("Parse(%s): %v", lang, err)
	}
	got := map[string]string{}
	for _, s := range syms {
		got[s.Name] = s.Kind
	}
	return got
}

func TestF5CodeintelLanguageJS(t *testing.T) {
	t.Parallel()
	got := symbolKinds(t, JS, `const MAX = 3;
let letOnly = 1;
var varOnly = 2;
function add(a, b) { return a + b; }
class Widget { render() {} get size() { return 1; } }
const arrow = (x) => x;
export function exp() {}
`)
	want := map[string]string{"MAX": "const", "add": "func", "Widget": "class", "render": "method", "size": "method", "arrow": "const", "exp": "func"}
	for name, kind := range want {
		if got[name] != kind {
			t.Fatalf("js symbols = %+v (want %s=%s)", got, name, kind)
		}
	}
	for _, name := range []string{"letOnly", "varOnly"} {
		if _, ok := got[name]; ok {
			t.Fatalf("js symbols = %+v (unexpected %s)", got, name)
		}
	}
}

func TestF5CodeintelLanguageTS(t *testing.T) {
	t.Parallel()
	got := symbolKinds(t, TS, `interface Shape { area(): number; }
type ID = string;
enum Color { Red }
class Circle implements Shape {
  radius = 1;
  area(): number { return 3; }
}
const make = (r: number): Circle => new Circle();
function id<T>(x: T): T { return x; }
`)
	want := map[string]string{"Shape": "type", "area": "method", "ID": "type", "Color": "enum", "Circle": "class", "make": "const", "id": "func"}
	for name, kind := range want {
		if got[name] != kind {
			t.Fatalf("ts symbols = %+v (want %s=%s)", got, name, kind)
		}
	}
	if _, ok := got["radius"]; ok {
		t.Fatalf("ts symbols = %+v (unexpected field)", got)
	}
	tsx := symbolKinds(t, TSX, "const App = () => <div/>;\nfunction View() { return <span/>; }\n")
	if tsx["App"] != "const" || tsx["View"] != "func" {
		t.Fatalf("tsx symbols = %+v", tsx)
	}
}

func TestF5CodeintelLanguagePython(t *testing.T) {
	t.Parallel()
	got := symbolKinds(t, Python, `import os
MAX = 3
class Widget:
    def render(self):
        def helper():
            pass
        return helper
    async def load(self):
        pass
def top():
    pass
`)
	want := map[string]string{"Widget": "class", "render": "method", "helper": "func", "load": "method", "top": "func"}
	for name, kind := range want {
		if got[name] != kind {
			t.Fatalf("python symbols = %+v (want %s=%s)", got, name, kind)
		}
	}
	if _, err := Parse(t.Context(), Python, []byte("def f(:\n\x00"), 0, 0); err != nil && !errors.Is(err, ErrParse) {
		t.Fatalf("python corrupt = %v", err)
	}
	if _, err := Parse(t.Context(), JS, []byte(strings.Repeat("x", MaxSourceBytes+1)), 0, 0); !errors.Is(err, ErrParse) {
		t.Fatalf("js oversize = %v", err)
	}
}

// TestF5CodeintelLanguageGrammarUnavailable proves the fail-closed seam: a
// missing grammar asset is an error for that file, never empty success.
func TestF5CodeintelLanguageGrammarUnavailable(t *testing.T) {
	t.Parallel()
	_, err := parseFS(t.Context(), JS, []byte("const x = 1;\n"), 0, 0, fstest.MapFS{})
	if !errors.Is(err, ErrParse) || !strings.Contains(err.Error(), "grammar asset") {
		t.Fatalf("missing grammar = %v", err)
	}
	if errors.Is(err, ErrUnsupportedLanguage) {
		t.Fatalf("missing grammar reported as unsupported: %v", err)
	}
}
