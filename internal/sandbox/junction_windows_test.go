package sandbox

import (
	"context"
	"errors"
	"os"
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

// TestJunctionOpen is S16-1: a junction resolving inside the root opens (the
// resolved path is free of reparse points, verified by the final path); one
// leaving the root is an outside_root verdict, opened only via OpenApproved.
func TestJunctionOpen(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	junction(t, filepath.Join(f.root, "sub"), filepath.Join(f.root, "jin"))
	junction(t, f.out, filepath.Join(f.root, "jout"))
	modes(t, f.sb, func(t *testing.T, sb *Sandbox) {
		file, v, err := sb.OpenFile(t.Context(), filepath.Join(f.root, "jin", "a.txt"), os.O_RDONLY, 0)
		if err != nil || v != Inside {
			t.Fatalf("OpenFile(junction in) = %v, %v", v, err)
		}
		if got := readAll(t, file); got != "a" {
			t.Errorf("read %q, want a", got)
		}
		out := filepath.Join(f.root, "jout", "secret.txt")
		got, v, err := sb.OpenFile(t.Context(), out, os.O_RDONLY, 0)
		if got != nil || err != nil || v != OutsideRoot {
			t.Fatalf("OpenFile(junction out) = %v, %v", v, err)
		}
		file, err = sb.OpenApproved(t.Context(), out, os.O_RDONLY, 0)
		if err != nil {
			t.Fatalf("OpenApproved(junction out): %v", err)
		}
		if got := readAll(t, file); got != "outside" {
			t.Errorf("read %q, want outside", got)
		}
	})
}

// TestOpenDriveAlias is S16-2 on Windows: a subst drive aliases the deny dir,
// so Check may see an outside_root path, but the handle's final path is the
// deny path and OpenApproved fails with ErrDeniedPath.
func TestOpenDriveAlias(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	drive := ""
	for _, l := range "PQRSTUVWXYZ" {
		if _, err := os.Stat(string(l) + `:\`); err != nil {
			drive = string(l) + ":"
			break
		}
	}
	if drive == "" {
		t.Fatal("no free drive letter for subst")
	}
	subst := func(args ...string) {
		cmd := exec.CommandContext(context.Background(), "subst", args...) //nolint:gosec // G204: fixed subst, args from the test
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("subst %v: %v: %s", args, err, out)
		}
	}
	subst(drive, f.home)
	t.Cleanup(func() { subst(drive, "/d") })
	alias := drive + `\.ssh\id_rsa`
	if v, _, err := f.sb.Check(t.Context(), alias, OpRead); !errors.Is(err, ErrDeniedPath) && (err != nil || v != OutsideRoot) {
		t.Fatalf("Check(subst alias) = %v, %v; want outside_root or denied_path", v, err)
	}
	if _, err := f.sb.OpenApproved(t.Context(), alias, os.O_RDONLY, 0); !errors.Is(err, ErrDeniedPath) {
		t.Errorf("OpenApproved(subst alias) = %v, want ErrDeniedPath", err)
	}
}
