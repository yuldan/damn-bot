package codetools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/sandbox"
)

// Corpus benchmarks, rg comparison and rg parity (CODE-TOOLS §10: CT-01,
// CT-02, CT-03, CT-10, CT-21). Corpora come from DAMNBOT_CORPUS_C1/C2/C3
// (bench/corpora.lock, `make corpora`), the oracle from DAMNBOT_RG
// (ripgrep 15.1.0, else rg on PATH); without them these tests and
// benchmarks skip, unless DAMNBOT_REQUIRE_CORPORA=1 (CI bench-codetools)
// turns the skip into a failure. Both sides use the same semantics:
// case-sensitive, ignore files honoured with or without a .git directory
// (rg --no-require-git), hidden entries excluded, literal queries are
// fixed strings (rg -F, QuoteMeta for RE2).

// benchQueries is bench/codetools/queries.json.
type benchQueries struct {
	Literal []string `json:"literal"`
	Regex   []string `json:"regex"`
	Glob    []string `json:"glob"`
	Parity  []string `json:"parity"`
}

func loadBenchQueries(tb testing.TB) *benchQueries {
	tb.Helper()
	var q benchQueries
	if err := json.Unmarshal(readFixture(tb, filepath.Join("..", "..", "bench", "codetools", "queries.json")), &q); err != nil {
		tb.Fatal(err)
	}
	return &q
}

// missing skips a test whose corpus or oracle is absent, or fails it when
// DAMNBOT_REQUIRE_CORPORA=1.
func missing(tb testing.TB, format string, args ...any) {
	tb.Helper()
	if os.Getenv("DAMNBOT_REQUIRE_CORPORA") == "1" {
		tb.Fatalf("DAMNBOT_REQUIRE_CORPORA=1: "+format, args...)
	}
	tb.Skipf(format, args...)
}

// corpusDir is the corpus root named by DAMNBOT_CORPUS_<id>.
func corpusDir(tb testing.TB, id string) string {
	tb.Helper()
	d := os.Getenv("DAMNBOT_CORPUS_" + id)
	if d == "" {
		missing(tb, "DAMNBOT_CORPUS_%s unset (make corpora, bench/corpora.lock)", id)
	}
	d, err := filepath.EvalSymlinks(d)
	if err != nil {
		tb.Fatal(err)
	}
	return d
}

// rgPath is the ripgrep 15 oracle.
func rgPath(tb testing.TB) string {
	tb.Helper()
	rg := os.Getenv("DAMNBOT_RG")
	if rg == "" {
		rg = "rg"
	}
	out, err := exec.CommandContext(tb.Context(), rg, "--version").Output() //nolint:gosec // G204: oracle binary chosen by the developer
	if err != nil || !bytes.HasPrefix(out, []byte("ripgrep 15.")) {
		missing(tb, "ripgrep 15 not found (DAMNBOT_RG=%q): %v", rg, err)
	}
	return rg
}

// specDenyPaths is the SPEC §12 deny_paths default (placeholders dropped).
func specDenyPaths() []string {
	return []string{
		"~/.ssh", "~/.gnupg", "~/.aws", "**/.env", "**/*.pem", "~/.netrc", "~/.git-credentials",
		"~/.config/gh", "~/.docker/config.json", "~/.kube", "/proc/*/environ", "/proc/*/task/*/environ", "/proc/*/mem",
	}
}

// corpusSandbox is a session sandbox over roots with the default deny list
// and the real home (global git excludes as rg reads them).
func corpusSandbox(tb testing.TB, roots ...string) *sandbox.Sandbox {
	tb.Helper()
	sb, err := sandbox.New(&sandbox.Config{Roots: roots, DenyPaths: specDenyPaths()})
	if err != nil {
		tb.Fatal(err)
	}
	return sb
}

// freshEngine is a new Engine (cold process caches) over sb.
func freshEngine(tb testing.TB, sb *sandbox.Sandbox, roots ...string) *Engine {
	tb.Helper()
	e, err := New(&Config{Sandbox: sb, Roots: roots})
	if err != nil {
		tb.Fatal(err)
	}
	return e
}

// unlimitedPage is the page cap of the CODE-TOOLS §10.1 comparison setting
// (with Limit=-1), so the whole output is formatted.
const unlimitedPage = math.MaxInt32

// corpusOp runs one comparison query through the engine API.
func corpusOp(ctx context.Context, e *Engine, kind, q string) (Result, error) {
	unlimited := Opts{PageChars: unlimitedPage}
	switch kind {
	case "literal":
		return e.Grep(ctx, &GrepArgs{Pattern: regexp.QuoteMeta(q), Case: "sensitive", Limit: -1}, unlimited)
	case "regex":
		return e.Grep(ctx, &GrepArgs{Pattern: q, Case: "sensitive", Limit: -1}, unlimited)
	}
	return e.Glob(ctx, GlobArgs{Pattern: q, Limit: -1}, unlimited)
}

// rgArgv is the equivalent rg command line.
func rgArgv(rg, kind, q string, extra ...string) []string {
	argv := []string{rg, "--no-config", "--no-require-git"}
	switch kind {
	case "literal":
		argv = append(argv, "-n", "-F", "-e", q)
	case "regex":
		argv = append(argv, "-n", "-e", q)
	default:
		argv = append(argv, "--files", "-g", q)
	}
	return append(argv, extra...)
}

// runRg runs rg; exit 1 (no match) is not an error.
func runRg(ctx context.Context, argv []string, dir string, stdout *bytes.Buffer) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // G204: oracle binary chosen by the developer
	cmd.Dir = dir
	if stdout != nil {
		cmd.Stdout = stdout
	}
	err := cmd.Run()
	if ee := (*exec.ExitError)(nil); err == nil || errors.As(err, &ee) && ee.ExitCode() == 1 {
		return nil
	}
	return fmt.Errorf("rg: %w", err)
}

func benchCorpus(b *testing.B, kind string, queries func(*benchQueries) []string) {
	q := queries(loadBenchQueries(b))
	for _, id := range []string{"C1", "C2", "C3"} {
		b.Run(id, func(b *testing.B) {
			root := corpusDir(b, id)
			sb := corpusSandbox(b, root)
			ctx := context.Background()
			for i := 0; b.Loop(); i++ {
				e := freshEngine(b, sb, root)
				if _, err := corpusOp(ctx, e, kind, q[i%len(q)]); err != nil {
					b.Fatal(err)
				}
				e.Close()
			}
		})
	}
}

// BenchmarkGrepLiteral is CT-01: one of the 40 literal queries per op; the
// rg ratio comes from TestCodetoolsProbe (bench/codetools/cmd/codetools).
func BenchmarkGrepLiteral(b *testing.B) {
	benchCorpus(b, "literal", func(q *benchQueries) []string { return q.Literal })
}

// BenchmarkGrepRegex is CT-02 over the 40 regex queries.
func BenchmarkGrepRegex(b *testing.B) {
	benchCorpus(b, "regex", func(q *benchQueries) []string { return q.Regex })
}

// BenchmarkGlob is CT-03 over the 10 glob queries.
func BenchmarkGlob(b *testing.B) {
	benchCorpus(b, "glob", func(q *benchQueries) []string { return q.Glob })
}

// hit is one (path, line) of a grep match set.
type hit struct {
	path string
	line int
}

var matchLineRe = regexp.MustCompile(`^(\d+):`)

// grepHits parses unlimited grep output into its (path, line) set.
func grepHits(content string) map[hit]bool {
	hits := map[hit]bool{}
	file := ""
	for i, l := range strings.Split(content, "\n") {
		switch m := matchLineRe.FindStringSubmatch(l); {
		case i == 0 || strings.HasPrefix(l, "skipped: "):
		case m != nil && file != "":
			if n, err := strconv.Atoi(m[1]); err == nil {
				hits[hit{file, n}] = true
			}
		default:
			if j := strings.LastIndex(l, " #"); j > 0 {
				file = l[:j]
			}
		}
	}
	return hits
}

// rgOraclePattern rewrites an RE2 pattern into the rg (Rust regex) pattern
// with the same semantics: Go's String() expands the ASCII Perl classes
// (\d \w \s are Unicode in rg) and anchors carry the (?m) the engine adds;
// \b and \B become ASCII word boundaries.
func rgOraclePattern(tb testing.TB, q string) string {
	tb.Helper()
	re, err := syntax.Parse("(?m)"+q, syntax.Perl)
	if err != nil {
		tb.Fatal(err)
	}
	s := re.String()
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s) && (s[i+1] == 'b' || s[i+1] == 'B'):
			b.WriteString(`(?-u:\` + s[i+1:i+2] + `)`)
			i++
		case s[i] == '\\' && i+1 < len(s):
			b.WriteString(s[i : i+2])
			i++
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// rgHits runs the rg oracle in root and parses `path\0line:` records. The
// oracle is normalized to the documented engine semantics: RE2 classes
// (rgOraclePattern), CRLF-aware anchors (--crlf: the engine matches lines
// without their '\r'), and hidden paths dropped (rg lets an ignore-file
// whitelist such as `!.gitignore` override hidden; CODE-TOOLS §2.3 skips
// hidden entries unless path or glob names them).
func rgHits(tb testing.TB, rg, root, kind, q string) map[hit]bool {
	tb.Helper()
	if kind == "regex" {
		q = rgOraclePattern(tb, q)
	}
	var out bytes.Buffer
	if err := runRg(tb.Context(), rgArgv(rg, kind, q, "--crlf", "--null"), root, &out); err != nil {
		tb.Fatalf("rg %q: %v", q, err)
	}
	hits := map[hit]bool{}
	sc := bufio.NewScanner(&out)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		p, rest, ok := strings.Cut(sc.Text(), "\x00")
		num, _, ok2 := strings.Cut(rest, ":")
		n, err := strconv.Atoi(num)
		if !ok || !ok2 || err != nil {
			tb.Fatalf("rg %q: bad record %q", q, sc.Text())
		}
		p = filepath.ToSlash(p)
		if !strings.HasPrefix(p, ".") && !strings.Contains(p, "/.") {
			hits[hit{p, n}] = true
		}
	}
	if err := sc.Err(); err != nil {
		tb.Fatal(err)
	}
	return hits
}

// TestGrepParityRg is CT-21: the (path, line) match set of grep equals
// rg --no-config for 200 queries (40 literal, 40 regex, 120 parity) on C1
// and C3.
func TestGrepParityRg(t *testing.T) {
	rg := rgPath(t)
	q := loadBenchQueries(t)
	type query struct{ kind, q string }
	var qs []query
	for _, l := range q.Literal {
		qs = append(qs, query{"literal", l})
	}
	for _, r := range slices.Concat(q.Regex, q.Parity) {
		qs = append(qs, query{"regex", r})
	}
	if len(qs) != 200 {
		t.Fatalf("%d parity queries, want 200", len(qs))
	}
	for _, id := range []string{"C1", "C3"} {
		t.Run(id, func(t *testing.T) {
			root := corpusDir(t, id)
			sb := corpusSandbox(t, root)
			e := freshEngine(t, sb, root)
			defer e.Close()
			for _, c := range qs {
				r, err := corpusOp(context.Background(), e, c.kind, c.q)
				if err != nil {
					t.Fatalf("%s %q: %v", c.kind, c.q, err)
				}
				got, want := grepHits(r.Content), rgHits(t, rg, root, c.kind, c.q)
				if diff := hitDiff(got, want); len(diff) > 0 {
					t.Errorf("%s %q: %d differences (ours %d, rg %d): %v", c.kind, c.q, len(diff), len(got), len(want), diff[:min(len(diff), 10)])
				}
			}
		})
	}
}

// hitDiff lists "+path:line" (only in got) and "-path:line" (only in
// want), sorted.
func hitDiff(got, want map[hit]bool) []string {
	var diff []string
	for h := range got {
		if !want[h] {
			diff = append(diff, fmt.Sprintf("+%s:%d", h.path, h.line))
		}
	}
	for h := range want {
		if !got[h] {
			diff = append(diff, fmt.Sprintf("-%s:%d", h.path, h.line))
		}
	}
	slices.Sort(diff)
	return diff
}

// percentiles returns the nearest-rank p50 and p95 in milliseconds (the
// bench/speed.Percentile definition).
func percentiles(ds []time.Duration) (p50, p95 float64) {
	s := slices.Clone(ds)
	slices.Sort(s)
	at := func(p float64) float64 {
		i := max(0, min(int(float64(len(s))*p/100+0.999999)-1, len(s)-1))
		return float64(s[i].Microseconds()) / 1000
	}
	return at(50), at(95)
}
