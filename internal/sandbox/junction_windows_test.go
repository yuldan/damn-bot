package sandbox

import (
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
)

// junction creates the directory junction link → target (mklink /J needs no
// privilege). A failure fails the test: junction cases are never skipped.
func junction(t *testing.T, target, link string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", link, target) //nolint:gosec // G204: fixed mklink, paths from t.TempDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mklink /J %s %s: %v: %s", link, target, err, out)
	}
}

// TestJunctionVerdicts is S15-2: junctions (ModeIrregular since Go 1.23,
// not followed by EvalSymlinks) are resolved like symlinks: one into a
// deny dir is denied_path, one leaving the root is outside_root, one
// staying inside resolves to its target.
func TestJunctionVerdicts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	junction(t, filepath.Join(f.home, ".ssh"), filepath.Join(f.root, "jssh"))
	junction(t, f.out, filepath.Join(f.root, "jout"))
	junction(t, filepath.Join(f.root, "sub"), filepath.Join(f.root, "jin"))
	if _, _, err := f.sb.Check(t.Context(), filepath.Join(f.root, "jssh", "id_rsa"), OpRead); !errors.Is(err, ErrDeniedPath) {
		t.Errorf("junction to ~/.ssh: err = %v, want ErrDeniedPath", err)
	}
	v, resolved, err := f.sb.Check(t.Context(), filepath.Join(f.root, "jout", "secret.txt"), OpRead)
	if err != nil || v != OutsideRoot || !sameWindowsPath(resolved, filepath.Join(f.out, "secret.txt")) {
		t.Errorf("junction out: %v %q %v, want outside_root at the target", v, resolved, err)
	}
	v, resolved, err = f.sb.Check(t.Context(), filepath.Join(f.root, "jin", "a.txt"), OpRead)
	if err != nil || v != Inside || !sameWindowsPath(resolved, filepath.Join(f.root, "sub", "a.txt")) {
		t.Errorf("junction in: %v %q %v, want inside at the target", v, resolved, err)
	}
	// A junction root canonicalizes to its target.
	sb, err := New(&Config{Roots: []string{filepath.Join(f.root, "jin")}, Home: f.home})
	if err != nil {
		t.Fatalf("New(junction root): %v", err)
	}
	if !sameWindowsPath(sb.roots[0].path, filepath.Join(f.root, "sub")) {
		t.Errorf("junction root = %q, want its target", sb.roots[0].path)
	}
}
