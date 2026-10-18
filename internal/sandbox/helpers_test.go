package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fixture is a root plus an outside dir and a fake home with ~/.ssh.
type fixture struct {
	root, out, home string
	sb              *Sandbox
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{root: realTemp(t), out: realTemp(t), home: realTemp(t)}
	blob := realTemp(t)
	mkfile(t, filepath.Join(f.root, "real.txt"), "inside")
	mkfile(t, filepath.Join(f.root, "sub", "a.txt"), "a")
	mkfile(t, filepath.Join(f.out, "secret.txt"), "outside")
	mkfile(t, filepath.Join(f.out, "outdir", "f.txt"), "outside-f")
	mkfile(t, filepath.Join(f.home, ".ssh", "id_rsa"), "key")
	mkfile(t, filepath.Join(blob, "b1"), "blob")
	sb, err := New(&Config{
		Roots:         []string{f.root},
		ReadOnlyExtra: []string{blob},
		DenyPaths:     []string{"~/.ssh", "**/.env", "*.pem", "/proc/*/environ", "/proc/*/task/*/environ", "/proc/*/mem"},
		Home:          f.home,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.sb = sb
	return f
}

// realTemp returns t.TempDir with symlinks resolved (macOS /var → /private/var).
func realTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return d
}

func mkfile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// symlink creates newname → oldname. A refusal fails the test, also on
// Windows (CI runners hold the symlink privilege): symlink cases are never
// skipped.
func symlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
}
