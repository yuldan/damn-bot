package evals

import (
	"strconv"
	"strings"
)

func langs() []*lang {
	return []*lang{goLang(), pyLang(), tsLang(), rustLang(), javaLang(), shLang()}
}

func goLang() *lang {
	l := &lang{
		name: "go", src: "solution.go", test: "solution_test.go", cmd: "go test ./...",
		header: "package solution\n\nimport (\n\t\"strconv\"\n\t\"strings\"\n)\n\n",
		impl: map[string]string{
			"sum_to":        "func SumTo(n int) int {\n\ts := 0\n\tfor i := 1; i <= n; i++ {\n\t\ts += i\n\t}\n\treturn s\n}\n",
			"max_of":        "func MaxOf(xs []int) int {\n\tm := xs[0]\n\tfor _, x := range xs[1:] {\n\t\tif x > m {\n\t\t\tm = x\n\t\t}\n\t}\n\treturn m\n}\n",
			"search":        "func Search(xs []int, x int) int {\n\tlo, hi := 0, len(xs)-1\n\tfor lo <= hi {\n\t\tmid := (lo + hi) / 2\n\t\tswitch {\n\t\tcase xs[mid] == x:\n\t\t\treturn mid\n\t\tcase xs[mid] < x:\n\t\t\tlo = mid + 1\n\t\tdefault:\n\t\t\thi = mid - 1\n\t\t}\n\t}\n\treturn -1\n}\n",
			"reverse":       "func Reverse(s string) string {\n\tr := []rune(s)\n\tout := make([]rune, 0, len(r))\n\tfor i := len(r) - 1; i >= 0; i-- {\n\t\tout = append(out, r[i])\n\t}\n\treturn string(out)\n}\n",
			"is_palindrome": "func IsPalindrome(s string) bool {\n\ts = strings.ToLower(s)\n\tr := []rune(s)\n\tfor i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {\n\t\tif r[i] != r[j] {\n\t\t\treturn false\n\t\t}\n\t}\n\treturn true\n}\n",
			"gcd":           "func Gcd(a, b int) int {\n\tif b == 0 {\n\t\treturn a\n\t}\n\treturn Gcd(b, a%b)\n}\n",
			"count_vowels":  "func CountVowels(s string) int {\n\tn := 0\n\tfor _, c := range strings.ToLower(s) {\n\t\tif strings.ContainsRune(\"aeiou\", c) {\n\t\t\tn++\n\t\t}\n\t}\n\treturn n\n}\n",
			"fizzbuzz":      "func Fizzbuzz(n int) string {\n\tswitch {\n\tcase n%15 == 0:\n\t\treturn \"FizzBuzz\"\n\tcase n%3 == 0:\n\t\treturn \"Fizz\"\n\tcase n%5 == 0:\n\t\treturn \"Buzz\"\n\t}\n\treturn strconv.Itoa(n)\n}\n",
			"clamp":         "func Clamp(x, lo, hi int) int {\n\tif x < lo {\n\t\treturn lo\n\t}\n\tif x > hi {\n\t\treturn hi\n\t}\n\treturn x\n}\n",
			"average":       "func Average(xs []int) float64 {\n\ts := 0\n\tfor _, x := range xs {\n\t\ts += x\n\t}\n\treturn float64(s) / float64(len(xs))\n}\n",
		},
		bug: map[string][2]string{
			"sum_to":        {"i <= n", "i < n"},
			"max_of":        {"m := xs[0]\n\tfor _, x := range xs[1:]", "m := 0\n\tfor _, x := range xs"},
			"search":        {"for lo <= hi", "for lo < hi"},
			"reverse":       {"i >= 0", "i > 0"},
			"is_palindrome": {"s = strings.ToLower(s)", "s = strings.TrimSpace(s)"},
			"gcd":           {"return a\n", "return b\n"},
			"count_vowels":  {`"aeiou"`, `"aeio"`},
			"fizzbuzz":      {"case n%15 == 0:\n\t\treturn \"FizzBuzz\"\n\tcase n%3 == 0:\n\t\treturn \"Fizz\"", "case n%3 == 0:\n\t\treturn \"Fizz\"\n\tcase n%15 == 0:\n\t\treturn \"FizzBuzz\""},
			"clamp":         {"if x > hi {\n\t\treturn hi", "if x > hi {\n\t\treturn lo"},
			"average":       {"float64(s) / float64(len(xs))", "float64(s / len(xs))"},
		},
	}
	l.testFile = func(calls string) string {
		return "package solution\n\nimport \"testing\"\n\nfunc TestSolution(t *testing.T) {\n" + calls + "}\n"
	}
	l.call = func(f fn, c tcase) string {
		r := func(v any) string {
			return lit(v, func(x []int) string { return "[]int{" + joinInts(x, ", ") + "}" }, strconv.FormatBool, fstr)
		}
		e := camel(f.key, true) + "(" + args(c, r) + ")"
		return "\tif got := " + e + "; got != " + r(c.want) + " {\n\t\tt.Errorf(\"%s = %v\", " + strconv.Quote(e) + ", got)\n\t}\n"
	}
	return l
}

func pyLang() *lang {
	l := &lang{
		name: "python", src: "solution.py", test: "test_solution.py", cmd: "python3 test_solution.py",
		impl: map[string]string{
			"sum_to":        "def sum_to(n):\n    s = 0\n    for i in range(1, n + 1):\n        s += i\n    return s\n",
			"max_of":        "def max_of(xs):\n    m = xs[0]\n    for x in xs[1:]:\n        if x > m:\n            m = x\n    return m\n",
			"search":        "def search(xs, x):\n    lo, hi = 0, len(xs) - 1\n    while lo <= hi:\n        mid = (lo + hi) // 2\n        if xs[mid] == x:\n            return mid\n        if xs[mid] < x:\n            lo = mid + 1\n        else:\n            hi = mid - 1\n    return -1\n",
			"reverse":       "def reverse(s):\n    return \"\".join(s[i] for i in range(len(s) - 1, -1, -1))\n",
			"is_palindrome": "def is_palindrome(s):\n    s = s.lower()\n    return s == s[::-1]\n",
			"gcd":           "def gcd(a, b):\n    if b == 0:\n        return a\n    return gcd(b, a % b)\n",
			"count_vowels":  "def count_vowels(s):\n    return sum(1 for c in s.lower() if c in \"aeiou\")\n",
			"fizzbuzz":      "def fizzbuzz(n):\n    if n % 15 == 0:\n        return \"FizzBuzz\"\n    if n % 3 == 0:\n        return \"Fizz\"\n    if n % 5 == 0:\n        return \"Buzz\"\n    return str(n)\n",
			"clamp":         "def clamp(x, lo, hi):\n    if x < lo:\n        return lo\n    if x > hi:\n        return hi\n    return x\n",
			"average":       "def average(xs):\n    return sum(xs) / len(xs)\n",
		},
		bug: map[string][2]string{
			"sum_to":        {"range(1, n + 1)", "range(1, n)"},
			"max_of":        {"m = xs[0]\n    for x in xs[1:]:", "m = 0\n    for x in xs:"},
			"search":        {"while lo <= hi", "while lo < hi"},
			"reverse":       {"len(s) - 1, -1, -1", "len(s) - 1, 0, -1"},
			"is_palindrome": {"s = s.lower()", "s = s.strip()"},
			"gcd":           {"return a\n", "return b\n"},
			"count_vowels":  {`"aeiou"`, `"aeio"`},
			"fizzbuzz":      {"if n % 15 == 0:\n        return \"FizzBuzz\"\n    if n % 3 == 0:\n        return \"Fizz\"", "if n % 3 == 0:\n        return \"Fizz\"\n    if n % 15 == 0:\n        return \"FizzBuzz\""},
			"clamp":         {"if x > hi:\n        return hi", "if x > hi:\n        return lo"},
			"average":       {"sum(xs) / len(xs)", "sum(xs) // len(xs)"},
		},
	}
	l.testFile = func(calls string) string {
		return "from solution import *\n\nfailed = 0\n\n\ndef check(expr, got, want):\n    global failed\n    if got != want:\n" +
			"        print(f\"FAIL {expr}: got {got!r}, want {want!r}\")\n        failed += 1\n\n\n" + calls + "\nraise SystemExit(1 if failed else 0)\n"
	}
	l.call = func(f fn, c tcase) string {
		r := func(v any) string {
			return lit(v, func(x []int) string { return "[" + joinInts(x, ", ") + "]" },
				func(b bool) string { return map[bool]string{true: "True", false: "False"}[b] }, fdot)
		}
		e := f.key + "(" + args(c, r) + ")"
		return "check(" + strconv.Quote(e) + ", " + e + ", " + r(c.want) + ")\n"
	}
	return l
}

func tsLang() *lang {
	l := &lang{
		name: "typescript", src: "solution.ts", test: "test_solution.ts", cmd: "tsc --strict --target es2022 --module commonjs --outDir out solution.ts test_solution.ts && node out/test_solution.js",
		impl: map[string]string{
			"sum_to":        "export function sumTo(n: number): number {\n  let s = 0;\n  for (let i = 1; i <= n; i++) {\n    s += i;\n  }\n  return s;\n}\n",
			"max_of":        "export function maxOf(xs: number[]): number {\n  let m = xs[0];\n  for (const x of xs.slice(1)) {\n    if (x > m) m = x;\n  }\n  return m;\n}\n",
			"search":        "export function search(xs: number[], x: number): number {\n  let lo = 0;\n  let hi = xs.length - 1;\n  while (lo <= hi) {\n    const mid = Math.floor((lo + hi) / 2);\n    if (xs[mid] === x) return mid;\n    if (xs[mid] < x) lo = mid + 1;\n    else hi = mid - 1;\n  }\n  return -1;\n}\n",
			"reverse":       "export function reverse(s: string): string {\n  let out = \"\";\n  for (let i = s.length - 1; i >= 0; i--) out += s[i];\n  return out;\n}\n",
			"is_palindrome": "export function isPalindrome(s: string): boolean {\n  const t = s.toLowerCase();\n  return t === [...t].reverse().join(\"\");\n}\n",
			"gcd":           "export function gcd(a: number, b: number): number {\n  return b === 0 ? a : gcd(b, a % b);\n}\n",
			"count_vowels":  "export function countVowels(s: string): number {\n  return [...s.toLowerCase()].filter((c) => \"aeiou\".includes(c)).length;\n}\n",
			"fizzbuzz":      "export function fizzbuzz(n: number): string {\n  if (n % 15 === 0) return \"FizzBuzz\";\n  if (n % 3 === 0) return \"Fizz\";\n  if (n % 5 === 0) return \"Buzz\";\n  return String(n);\n}\n",
			"clamp":         "export function clamp(x: number, lo: number, hi: number): number {\n  if (x < lo) return lo;\n  if (x > hi) return hi;\n  return x;\n}\n",
			"average":       "export function average(xs: number[]): number {\n  const s = xs.reduce((a, b) => a + b, 0);\n  return s / xs.length;\n}\n",
		},
		bug: map[string][2]string{
			"sum_to":        {"i <= n", "i < n"},
			"max_of":        {"let m = xs[0];\n  for (const x of xs.slice(1))", "let m = 0;\n  for (const x of xs)"},
			"search":        {"while (lo <= hi)", "while (lo < hi)"},
			"reverse":       {"i >= 0", "i > 0"},
			"is_palindrome": {"s.toLowerCase();\n  return t", "s.trim();\n  return t"},
			"gcd":           {"? a :", "? b :"},
			"count_vowels":  {`"aeiou"`, `"aeio"`},
			"fizzbuzz":      {"if (n % 15 === 0) return \"FizzBuzz\";\n  if (n % 3 === 0) return \"Fizz\";", "if (n % 3 === 0) return \"Fizz\";\n  if (n % 15 === 0) return \"FizzBuzz\";"},
			"clamp":         {"if (x > hi) return hi;", "if (x > hi) return lo;"},
			"average":       {"return s / xs.length;", "return Math.trunc(s / xs.length);"},
		},
	}
	l.testFile = func(calls string) string {
		return "import * as s from \"./solution.js\";\n\nlet failed = 0;\n\nfunction check(expr: string, got: unknown, want: unknown): void {\n" +
			"  if (got !== want) {\n    console.log(`FAIL ${expr}: got ${String(got)}, want ${String(want)}`);\n    failed++;\n  }\n}\n\n" +
			calls + "\nif (failed > 0) {\n  throw new Error(`${failed} check(s) failed`);\n}\n"
	}
	l.call = func(f fn, c tcase) string {
		r := func(v any) string {
			return lit(v, func(x []int) string { return "[" + joinInts(x, ", ") + "]" }, strconv.FormatBool, fstr)
		}
		e := "s." + camel(f.key, false) + "(" + args(c, r) + ")"
		return "check(" + strconv.Quote(e) + ", " + e + ", " + r(c.want) + ");\n"
	}
	return l
}

func rustLang() *lang {
	l := &lang{
		name: "rust", src: "solution.rs", test: "tests.rs", cmd: "rustc --edition 2021 --test tests.rs -o tests && ./tests",
		impl: map[string]string{
			"sum_to":        "pub fn sum_to(n: i64) -> i64 {\n    let mut s = 0;\n    for i in 1..=n {\n        s += i;\n    }\n    s\n}\n",
			"max_of":        "pub fn max_of(xs: &[i64]) -> i64 {\n    let mut m = xs[0];\n    for &x in &xs[1..] {\n        if x > m {\n            m = x;\n        }\n    }\n    m\n}\n",
			"search":        "pub fn search(xs: &[i64], x: i64) -> i64 {\n    let (mut lo, mut hi) = (0i64, xs.len() as i64 - 1);\n    while lo <= hi {\n        let mid = (lo + hi) / 2;\n        let v = xs[mid as usize];\n        if v == x {\n            return mid;\n        } else if v < x {\n            lo = mid + 1;\n        } else {\n            hi = mid - 1;\n        }\n    }\n    -1\n}\n",
			"reverse":       "pub fn reverse(s: &str) -> String {\n    let c: Vec<char> = s.chars().collect();\n    let mut out = String::new();\n    let mut i = c.len();\n    while i > 0 {\n        i -= 1;\n        out.push(c[i]);\n    }\n    out\n}\n",
			"is_palindrome": "pub fn is_palindrome(s: &str) -> bool {\n    let t: Vec<char> = s.to_lowercase().chars().collect();\n    t.iter().eq(t.iter().rev())\n}\n",
			"gcd":           "pub fn gcd(a: i64, b: i64) -> i64 {\n    if b == 0 {\n        a\n    } else {\n        gcd(b, a % b)\n    }\n}\n",
			"count_vowels":  "pub fn count_vowels(s: &str) -> i64 {\n    s.to_lowercase().chars().filter(|c| \"aeiou\".contains(*c)).count() as i64\n}\n",
			"fizzbuzz":      "pub fn fizzbuzz(n: i64) -> String {\n    if n % 15 == 0 {\n        return \"FizzBuzz\".to_string();\n    }\n    if n % 3 == 0 {\n        return \"Fizz\".to_string();\n    }\n    if n % 5 == 0 {\n        return \"Buzz\".to_string();\n    }\n    n.to_string()\n}\n",
			"clamp":         "pub fn clamp(x: i64, lo: i64, hi: i64) -> i64 {\n    if x < lo {\n        return lo;\n    }\n    if x > hi {\n        return hi;\n    }\n    x\n}\n",
			"average":       "pub fn average(xs: &[i64]) -> f64 {\n    let s: i64 = xs.iter().sum();\n    s as f64 / xs.len() as f64\n}\n",
		},
		bug: map[string][2]string{
			"sum_to":        {"1..=n", "1..n"},
			"max_of":        {"let mut m = xs[0];\n    for &x in &xs[1..]", "let mut m = 0;\n    for &x in xs"},
			"search":        {"while lo <= hi", "while lo < hi"},
			"reverse":       {"while i > 0", "while i > 1"},
			"is_palindrome": {"s.to_lowercase().chars().collect();\n    t.iter()", "s.trim().chars().collect();\n    t.iter()"},
			"gcd":           {"        a\n", "        b\n"},
			"count_vowels":  {`"aeiou"`, `"aeio"`},
			"fizzbuzz":      {"if n % 15 == 0 {\n        return \"FizzBuzz\".to_string();\n    }\n    if n % 3 == 0 {\n        return \"Fizz\".to_string();", "if n % 3 == 0 {\n        return \"Fizz\".to_string();\n    }\n    if n % 15 == 0 {\n        return \"FizzBuzz\".to_string();"},
			"clamp":         {"if x > hi {\n        return hi;", "if x > hi {\n        return lo;"},
			"average":       {"s as f64 / xs.len() as f64", "(s / xs.len() as i64) as f64"},
		},
	}
	l.testFile = func(calls string) string {
		return "include!(\"solution.rs\");\n\n#[test]\nfn solution() {\n" + calls + "}\n"
	}
	l.call = func(f fn, c tcase) string {
		r := func(v any) string {
			return lit(v, func(x []int) string { return "&[" + joinInts(x, ", ") + "]" }, strconv.FormatBool, fdot)
		}
		return "    assert_eq!(" + f.key + "(" + args(c, r) + "), " + r(c.want) + ");\n"
	}
	return l
}

func javaLang() *lang {
	l := &lang{
		name: "java", src: "Solution.java", test: "SolutionTest.java", cmd: "javac Solution.java SolutionTest.java && java SolutionTest",
		header: "public class Solution {\n", footer: "}\n",
		impl: map[string]string{
			"sum_to":        "    public static int sumTo(int n) {\n        int s = 0;\n        for (int i = 1; i <= n; i++) {\n            s += i;\n        }\n        return s;\n    }\n",
			"max_of":        "    public static int maxOf(int[] xs) {\n        int m = xs[0];\n        for (int i = 1; i < xs.length; i++) {\n            if (xs[i] > m) {\n                m = xs[i];\n            }\n        }\n        return m;\n    }\n",
			"search":        "    public static int search(int[] xs, int x) {\n        int lo = 0, hi = xs.length - 1;\n        while (lo <= hi) {\n            int mid = (lo + hi) / 2;\n            if (xs[mid] == x) {\n                return mid;\n            } else if (xs[mid] < x) {\n                lo = mid + 1;\n            } else {\n                hi = mid - 1;\n            }\n        }\n        return -1;\n    }\n",
			"reverse":       "    public static String reverse(String s) {\n        StringBuilder out = new StringBuilder();\n        for (int i = s.length() - 1; i >= 0; i--) {\n            out.append(s.charAt(i));\n        }\n        return out.toString();\n    }\n",
			"is_palindrome": "    public static boolean isPalindrome(String s) {\n        String t = s.toLowerCase();\n        return new StringBuilder(t).reverse().toString().equals(t);\n    }\n",
			"gcd":           "    public static int gcd(int a, int b) {\n        if (b == 0) {\n            return a;\n        }\n        return gcd(b, a % b);\n    }\n",
			"count_vowels":  "    public static int countVowels(String s) {\n        int n = 0;\n        for (char c : s.toLowerCase().toCharArray()) {\n            if (\"aeiou\".indexOf(c) >= 0) {\n                n++;\n            }\n        }\n        return n;\n    }\n",
			"fizzbuzz":      "    public static String fizzbuzz(int n) {\n        if (n % 15 == 0) {\n            return \"FizzBuzz\";\n        }\n        if (n % 3 == 0) {\n            return \"Fizz\";\n        }\n        if (n % 5 == 0) {\n            return \"Buzz\";\n        }\n        return Integer.toString(n);\n    }\n",
			"clamp":         "    public static int clamp(int x, int lo, int hi) {\n        if (x < lo) {\n            return lo;\n        }\n        if (x > hi) {\n            return hi;\n        }\n        return x;\n    }\n",
			"average":       "    public static double average(int[] xs) {\n        int s = 0;\n        for (int x : xs) {\n            s += x;\n        }\n        return (double) s / xs.length;\n    }\n",
		},
		bug: map[string][2]string{
			"sum_to":        {"i <= n", "i < n"},
			"max_of":        {"int m = xs[0];\n        for (int i = 1;", "int m = 0;\n        for (int i = 0;"},
			"search":        {"while (lo <= hi)", "while (lo < hi)"},
			"reverse":       {"i >= 0", "i > 0"},
			"is_palindrome": {"String t = s.toLowerCase();", "String t = s.trim();"},
			"gcd":           {"return a;", "return b;"},
			"count_vowels":  {`"aeiou"`, `"aeio"`},
			"fizzbuzz":      {"if (n % 15 == 0) {\n            return \"FizzBuzz\";\n        }\n        if (n % 3 == 0) {\n            return \"Fizz\";", "if (n % 3 == 0) {\n            return \"Fizz\";\n        }\n        if (n % 15 == 0) {\n            return \"FizzBuzz\";"},
			"clamp":         {"if (x > hi) {\n            return hi;", "if (x > hi) {\n            return lo;"},
			"average":       {"return (double) s / xs.length;", "return s / xs.length;"},
		},
	}
	l.testFile = func(calls string) string {
		return "public class SolutionTest {\n    static int failed = 0;\n\n    static void check(String expr, Object got, Object want) {\n" +
			"        if (!got.equals(want)) {\n            System.out.println(\"FAIL \" + expr + \": got \" + got + \", want \" + want);\n            failed++;\n        }\n    }\n\n" +
			"    public static void main(String[] args) {\n" + calls + "        System.exit(failed == 0 ? 0 : 1);\n    }\n}\n"
	}
	l.call = func(f fn, c tcase) string {
		r := func(v any) string {
			return lit(v, func(x []int) string { return "new int[]{" + joinInts(x, ", ") + "}" }, strconv.FormatBool, fdot)
		}
		e := "Solution." + camel(f.key, false) + "(" + args(c, r) + ")"
		return "        check(" + strconv.Quote(e) + ", " + e + ", " + r(c.want) + ");\n"
	}
	return l
}

func shLang() *lang {
	l := &lang{
		name: "shell", src: "solution.sh", test: "test.sh", cmd: "bash test.sh",
		header: "#!/usr/bin/env bash\n# Functions print their result on stdout.\n\n",
		impl: map[string]string{
			"sum_to":        "sum_to() {\n  local n=$1 s=0 i\n  for ((i = 1; i <= n; i++)); do\n    s=$((s + i))\n  done\n  echo \"$s\"\n}\n",
			"max_of":        "max_of() {\n  local m=$1 x\n  shift\n  for x in \"$@\"; do\n    if ((x > m)); then\n      m=$x\n    fi\n  done\n  echo \"$m\"\n}\n",
			"search":        "search() {\n  local x=$1\n  shift\n  local -a xs=(\"$@\")\n  local lo=0 hi=$((${#xs[@]} - 1)) mid\n  while ((lo <= hi)); do\n    mid=$(((lo + hi) / 2))\n    if ((xs[mid] == x)); then\n      echo \"$mid\"\n      return\n    elif ((xs[mid] < x)); then\n      lo=$((mid + 1))\n    else\n      hi=$((mid - 1))\n    fi\n  done\n  echo -1\n}\n",
			"reverse":       "reverse() {\n  local s=$1 out=\"\" i\n  for ((i = ${#s} - 1; i >= 0; i--)); do\n    out+=${s:i:1}\n  done\n  echo \"$out\"\n}\n",
			"is_palindrome": "is_palindrome() {\n  local t=${1,,} r=\"\" i\n  for ((i = ${#t} - 1; i >= 0; i--)); do\n    r+=${t:i:1}\n  done\n  if [[ $t == \"$r\" ]]; then echo true; else echo false; fi\n}\n",
			"gcd":           "gcd() {\n  local a=$1 b=$2\n  if ((b == 0)); then\n    echo \"$a\"\n  else\n    gcd \"$b\" $((a % b))\n  fi\n}\n",
			"count_vowels":  "count_vowels() {\n  local t=${1,,}\n  t=${t//[^aeiou]/}\n  echo \"${#t}\"\n}\n",
			"fizzbuzz":      "fizzbuzz() {\n  local n=$1\n  if ((n % 15 == 0)); then\n    echo FizzBuzz\n  elif ((n % 3 == 0)); then\n    echo Fizz\n  elif ((n % 5 == 0)); then\n    echo Buzz\n  else\n    echo \"$n\"\n  fi\n}\n",
			"clamp":         "clamp() {\n  local x=$1 lo=$2 hi=$3\n  if ((x < lo)); then\n    echo \"$lo\"\n  elif ((x > hi)); then\n    echo \"$hi\"\n  else\n    echo \"$x\"\n  fi\n}\n",
			"average":       "average() {\n  local s=0 x\n  for x in \"$@\"; do\n    s=$((s + x))\n  done\n  awk -v s=\"$s\" -v n=\"$#\" 'BEGIN { print s / n }'\n}\n",
		},
		bug: map[string][2]string{
			"sum_to":        {"i <= n", "i < n"},
			"max_of":        {"local m=$1 x\n  shift\n", "local m=0 x\n"},
			"search":        {"while ((lo <= hi))", "while ((lo < hi))"},
			"reverse":       {"for ((i = ${#s} - 1; i >= 0; i--))", "for ((i = ${#s} - 1; i > 0; i--))"},
			"is_palindrome": {"local t=${1,,} r=", "local t=$1 r="},
			"gcd":           {"echo \"$a\"", "echo \"$b\""},
			"count_vowels":  {"[^aeiou]", "[^aeio]"},
			"fizzbuzz":      {"if ((n % 15 == 0)); then\n    echo FizzBuzz\n  elif ((n % 3 == 0)); then\n    echo Fizz\n", "if ((n % 3 == 0)); then\n    echo Fizz\n  elif ((n % 15 == 0)); then\n    echo FizzBuzz\n"},
			"clamp":         {"echo \"$hi\"", "echo \"$lo\""},
			"average":       {"awk -v s=\"$s\" -v n=\"$#\" 'BEGIN { print s / n }'", "echo $((s / $#))"},
		},
	}
	l.testFile = func(calls string) string {
		return "#!/usr/bin/env bash\nsource ./solution.sh\nfailed=0\ncheck() {\n  if [[ $2 != \"$3\" ]]; then\n    echo \"FAIL $1: got $2, want $3\"\n    failed=1\n  fi\n}\n\n" +
			calls + "\nexit \"$failed\"\n"
	}
	l.call = func(f fn, c tcase) string {
		w := func(v any) string {
			return lit(v, func(x []int) string { return joinInts(x, " ") }, strconv.FormatBool, fstr)
		}
		a := make([]string, 0, len(c.args))
		for _, v := range c.args {
			if s, ok := v.(string); ok {
				a = append(a, s)
			} else {
				a = append(a, w(v))
			}
		}
		if f.xLast { // shell takes the scalar first: search x xs...
			a = append([]string{a[len(a)-1]}, a[:len(a)-1]...)
		}
		e := strings.Join(append([]string{f.key}, a...), " ")
		want := w(c.want)
		if s, ok := c.want.(string); ok {
			want = s
		}
		return "check '" + e + "' \"$(" + e + ")\" '" + want + "'\n"
	}
	return l
}
