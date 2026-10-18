package codetools

import (
	"context"
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

func newEnv(t testing.TB) *env { return newEnvDeny(t, "**/.env", "*.pem") }

// newEnvDeny is newEnv with the given deny_paths ("~/" = the fake home).
func newEnvDeny(t testing.TB, deny ...string) *env {
	t.Helper()
	v := &env{root: realTemp(t), out: realTemp(t), home: realTemp(t)}
	sb, err := sandbox.New(&sandbox.Config{Roots: []string{v.root}, DenyPaths: deny, Home: v.home})
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}
	v.sb = sb
	e, err := New(&Config{Sandbox: sb, Roots: []string{v.root}})
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

func (v *env) put(t testing.TB, rel, content string) {
	t.Helper()
	writeFile(t, filepath.Join(v.root, filepath.FromSlash(rel)), content)
}

func (v *env) read(t testing.TB, a ReadArgs) Result {
	t.Helper()
	r, err := v.e.Read(context.Background(), a, Opts{})
	if err != nil {
		t.Fatalf("Read(%+v): %v", a, err)
	}
	return r
}

func wantCode(t testing.TB, err error, c Code) {
	t.Helper()
	if got := codeOf(err); got != c {
		t.Fatalf("code = %q, want %q (err %v)", got, c, err)
	}
}

// firstLine is the header line of a result.
func firstLine(s string) string { h, _, _ := strings.Cut(s, "\n"); return h }
