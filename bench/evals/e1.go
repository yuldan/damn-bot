package evals

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// E1 internal: 10 bug kinds × 6 languages. Each task ships a source file with
// ten functions (exactly one buggy) and a test file; the grader runs the
// tests and requires the test file to stay unchanged.

// fn is one function under test; cases are shared by all languages.
type fn struct {
	key   string // snake_case name; also the bug kind
	cases []tcase
	xLast bool // shell: the scalar argument is passed first (search x xs...)
}

type tcase struct {
	args []any // int, []int, string
	want any   // int, string, bool, float64
}

var fns = []fn{
	{key: "sum_to", cases: []tcase{{[]any{10}, 55}, {[]any{1}, 1}}},
	{key: "max_of", cases: []tcase{{[]any{[]int{-5, -2, -9}}, -2}, {[]any{[]int{3, 7, 1}}, 7}}},
	{key: "search", xLast: true, cases: []tcase{
		{[]any{[]int{1, 3, 5, 7, 9}, 9}, 4}, {[]any{[]int{1, 3, 5, 7, 9}, 1}, 0}, {[]any{[]int{1, 3, 5, 7, 9}, 4}, -1},
	}},
	{key: "reverse", cases: []tcase{{[]any{"abc"}, "cba"}, {[]any{"a"}, "a"}}},
	{key: "is_palindrome", cases: []tcase{{[]any{"Level"}, true}, {[]any{"abc"}, false}}},
	{key: "gcd", cases: []tcase{{[]any{12, 18}, 6}, {[]any{7, 5}, 1}}},
	{key: "count_vowels", cases: []tcase{{[]any{"education"}, 5}, {[]any{"sky"}, 0}}},
	{key: "fizzbuzz", cases: []tcase{{[]any{15}, "FizzBuzz"}, {[]any{9}, "Fizz"}, {[]any{10}, "Buzz"}, {[]any{7}, "7"}}},
	{key: "clamp", cases: []tcase{{[]any{5, 0, 3}, 3}, {[]any{-1, 0, 3}, 0}, {[]any{2, 0, 3}, 2}}},
	{key: "average", cases: []tcase{{[]any{[]int{1, 2}}, 1.5}, {[]any{[]int{2, 4, 6}}, 4.0}}},
}

// lang renders sources and tests for one language.
type lang struct {
	name, src, test, cmd string
	header, footer       string
	impl                 map[string]string    // correct function bodies keyed by fn.key
	bug                  map[string][2]string // fn.key -> {old, new} applied once to impl
	testFile             func(calls string) string
	call                 func(f fn, c tcase) string
}

func camel(key string, upperFirst bool) string {
	parts := strings.Split(key, "_")
	for i, p := range parts {
		if i > 0 || upperFirst {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

func fstr(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// fdot renders a float with a decimal point (Rust/Java/Python literals).
func fdot(f float64) string {
	s := fstr(f)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}

func lit(v any, list func([]int) string, b func(bool) string, fl func(float64) string) string {
	switch x := v.(type) {
	case int:
		return strconv.Itoa(x)
	case []int:
		return list(x)
	case string:
		return strconv.Quote(x)
	case bool:
		return b(x)
	case float64:
		return fl(x)
	}
	return "" // unreachable: checkCases rejects other types before rendering
}

// checkCases rejects literal types lit cannot render.
func checkCases() error {
	for _, f := range fns {
		for _, c := range f.cases {
			for _, v := range append(slices.Clone(c.args), c.want) {
				switch v.(type) {
				case int, []int, string, bool, float64:
				default:
					return fmt.Errorf("evals: %s: unsupported literal %T", f.key, v)
				}
			}
		}
	}
	return nil
}

func joinInts(xs []int, sep string) string {
	s := make([]string, len(xs))
	for i, x := range xs {
		s[i] = strconv.Itoa(x)
	}
	return strings.Join(s, sep)
}

func args(c tcase, r func(any) string) string {
	s := make([]string, len(c.args))
	for i, a := range c.args {
		s[i] = r(a)
	}
	return strings.Join(s, ", ")
}

func e1Internal() ([]Task, error) {
	if err := checkCases(); err != nil {
		return nil, err
	}
	var out []Task
	n := 101
	for _, l := range langs() {
		for _, f := range fns {
			t, err := e1Task(fmt.Sprintf("E1-%03d", n), l, f.key)
			if err != nil {
				return nil, err
			}
			out = append(out, t)
			n++
		}
	}
	return out, nil
}

func e1Task(id string, l *lang, buggy string) (Task, error) {
	var good, bad strings.Builder
	good.WriteString(l.header)
	for i, f := range fns {
		if i > 0 {
			good.WriteString("\n")
		}
		good.WriteString(l.impl[f.key])
	}
	good.WriteString(l.footer)
	b := l.bug[buggy]
	if strings.Count(good.String(), b[0]) != 1 {
		return Task{}, fmt.Errorf("evals: %s/%s: bug anchor %q must occur exactly once", l.name, buggy, b[0])
	}
	bad.WriteString(strings.Replace(good.String(), b[0], b[1], 1))
	var calls strings.Builder
	for _, f := range fns {
		for _, c := range f.cases {
			calls.WriteString(l.call(f, c))
		}
	}
	files := map[string]string{l.src: bad.String(), l.test: l.testFile(calls.String())}
	if l.name == "go" {
		files["go.mod"] = "module solution\n\ngo 1.22\n"
	}
	return Task{
		ID: id, Set: "E1", Kind: "internal-" + l.name + ":" + buggy, Source: "internal",
		Prompt: fmt.Sprintf("The test suite fails (%#q). Find and fix the bug in %s. Do not modify %s.", l.cmd, l.src, l.test),
		Files:  files,
		Grader: Grader{Type: "test", Command: l.cmd, Unchanged: []string{l.test}, Reference: map[string]string{l.src: good.String()}},
	}, nil
}
