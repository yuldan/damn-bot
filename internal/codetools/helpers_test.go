package codetools

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/damnbot/damnbot/internal/sandbox"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// env is one session: a root, an outside dir, a fake home and an Engine.
type env struct {
	root, out, home string
	sb              *sandbox.Sandbox
	e               *Engine
}

func newEnv(t testing.TB) *env {
	t.Helper()
	v := &env{root: realTemp(t), out: realTemp(t), home: realTemp(t)}
	sb, err := sandbox.New(&sandbox.Config{Roots: []string{v.root}, DenyPaths: []string{"**/.env", "*.pem"}, Home: v.home})
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}
	v.sb = sb
	e, err := New(&Config{Sandbox: sb, Roots: []string{v.root}, Home: v.home, XDGConfigHome: filepath.Join(v.home, ".config")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(e.Close)
	v.e = e
	return v
}

// realTemp is t.TempDir with symlinks resolved (macOS /var → /private/var).
func realTemp(t testing.TB) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return d
}

func writeFile(t testing.TB, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func (v *env) put(t testing.TB, rel, content string) string {
	t.Helper()
	p := filepath.Join(v.root, filepath.FromSlash(rel))
	writeFile(t, p, content)
	return p
}

func (v *env) get(t testing.TB, rel string) string {
	t.Helper()
	return string(readFixture(t, filepath.Join(v.root, filepath.FromSlash(rel))))
}

func (v *env) read(t testing.TB, a ReadArgs) Result {
	t.Helper()
	r, err := v.e.Read(context.Background(), a, Opts{})
	if err != nil {
		t.Fatalf("Read(%+v): %v", a, err)
	}
	return r
}

// tag reads rel and returns its tag.
func (v *env) tag(t testing.TB, rel string) string {
	t.Helper()
	r := v.read(t, ReadArgs{Path: rel})
	s := metaStr(&r, "tag")
	if s == "" {
		t.Fatalf("%s: no tag", rel)
	}
	return s
}

func wantCode(t testing.TB, err error, c Code) {
	t.Helper()
	if got := codeOf(err); got != c {
		t.Fatalf("code = %q, want %q (err %v)", got, c, err)
	}
}

// testRand is deterministic test data (seeded), never secrets.
func testRand(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, seed)) //nolint:gosec // G404: deterministic test data, not secrets
}

// readFixture reads a file this test created.
func readFixture(t testing.TB, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p) //nolint:gosec // G304: fixture paths of this test
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return b
}

// metaStr is a string Meta field of r ("" when absent).
func metaStr(r *Result, key string) string { //nolint:unparam // generic Meta accessor
	s, ok := r.Meta[key].(string)
	if !ok {
		return ""
	}
	return s
}

// asToolErr returns err as *ToolError or fails the test.
func asToolErr(t testing.TB, err error) *ToolError {
	t.Helper()
	te, ok := errors.AsType[*ToolError](err)
	if !ok {
		t.Fatalf("not a ToolError: %v", err)
	}
	return te
}

// firstLine is the header line of a result.
func firstLine(s string) string { h, _, _ := strings.Cut(s, "\n"); return h }

// bytes reports the cached payload bytes (CT-35 tests).
func (c *caches) bytes() int {
	return c.read.used + c.index.used + c.snaps.used + c.outlines.used
}
