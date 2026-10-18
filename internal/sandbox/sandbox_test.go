package sandbox

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodeToolsSymlinkOutsideRoot(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	r := func(p ...string) string { return filepath.Join(append([]string{f.root}, p...)...) }
	symlink(t, filepath.Join(f.out, "secret.txt"), r("link_file"))
	symlink(t, filepath.Join(f.out, "outdir"), r("link_dir"))
	symlink(t, filepath.Join(f.home, ".ssh", "id_rsa"), r("link_deny"))
	symlink(t, filepath.Join(f.home, ".ssh"), r("link_denydir"))
	symlink(t, "real.txt", r("link_inside"))
	symlink(t, filepath.Join(f.out, "new.txt"), r("dangling"))
	symlink(t, "../sub", r("sub", "up"))
	symlink(t, "loop", r("loop"))

	tests := []struct {
		name, path   string
		op           Op
		want         Verdict
		wantErr      error
		wantResolved string
	}{
		{"inside", r("real.txt"), OpRead, Inside, nil, r("real.txt")},
		{"file link read", r("link_file"), OpRead, OutsideRoot, nil, filepath.Join(f.out, "secret.txt")},
		{"file link write", r("link_file"), OpWrite, OutsideRoot, nil, filepath.Join(f.out, "secret.txt")},
		{"dir link middle component", r("link_dir", "f.txt"), OpRead, OutsideRoot, nil, filepath.Join(f.out, "outdir", "f.txt")},
		{"dir link new file", r("link_dir", "new.txt"), OpWrite, OutsideRoot, nil, filepath.Join(f.out, "outdir", "new.txt")},
		{"link to deny file", r("link_deny"), OpRead, OutsideRoot, ErrDeniedPath, ""},
		{"link to deny dir", r("link_denydir", "id_rsa"), OpWrite, OutsideRoot, ErrDeniedPath, ""},
		{"link inside root", r("link_inside"), OpRead, Inside, nil, r("real.txt")},
		{"relative link in subdir", r("sub", "up", "a.txt"), OpRead, Inside, nil, r("sub", "a.txt")},
		{"dotdot traversal", r("sub", "..", "..", filepath.Base(f.out), "secret.txt"), OpRead, OutsideRoot, nil, filepath.Join(f.out, "secret.txt")},
		{"absolute outside", filepath.Join(f.out, "secret.txt"), OpRead, OutsideRoot, nil, filepath.Join(f.out, "secret.txt")},
		{"new nested write", r("new", "dir", "x.txt"), OpWrite, Inside, nil, r("new", "dir", "x.txt")},
		{"dangling link write", r("dangling"), OpWrite, OutsideRoot, nil, filepath.Join(f.out, "new.txt")},
		{"root itself", f.root, OpRead, Inside, nil, f.root},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v, resolved, err := f.sb.Check(t.Context(), tc.path, tc.op)
			if !errors.Is(err, tc.wantErr) || v != tc.want || resolved != tc.wantResolved {
				t.Fatalf("Check(%q) = %v, %q, %v; want %v, %q, %v", tc.path, v, resolved, err, tc.want, tc.wantResolved, tc.wantErr)
			}
		})
	}

	t.Run("symlink loop errors", func(t *testing.T) {
		t.Parallel()
		if _, _, err := f.sb.Check(t.Context(), r("loop"), OpRead); err == nil || errors.Is(err, ErrDeniedPath) {
			t.Fatalf("loop: err = %v, want resolve error", err)
		}
	})
}

func TestDenyPaths(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	folded := *f.sb
	folded.foldDeny = true
	tests := []struct {
		name string
		sb   *Sandbox
		path string
		deny bool
	}{
		{"proc self environ", f.sb, "/proc/self/environ", true},
		{"proc pid environ", f.sb, "/proc/123/environ", true},
		{"proc task environ", f.sb, "/proc/123/task/45/environ", true},
		{"proc mem", f.sb, "/proc/1/mem", true},
		{"proc status allowed", f.sb, "/proc/1/status", false},
		{"home ssh key", f.sb, filepath.Join(f.home, ".ssh", "id_rsa"), true},
		{"home ssh dir", f.sb, filepath.Join(f.home, ".ssh"), true},
		{"ssh lookalike", f.sb, filepath.Join(f.home, ".sshx"), false},
		{"dotenv in root", f.sb, filepath.Join(f.root, "app", ".env"), true},
		{"dotenv example", f.sb, filepath.Join(f.root, ".env.example"), false},
		{"pem relative pattern", f.sb, filepath.Join(f.root, "certs", "key.pem"), true},
		{"case sensitive", f.sb, filepath.Join(f.home, ".SSH", "id_rsa"), f.sb.foldDeny},
		{"case folded", &folded, filepath.Join(f.home, ".SSH", "ID_RSA"), true},
		{"case folded dotenv", &folded, filepath.Join(f.root, ".ENV"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := tc.sb.Check(t.Context(), tc.path, OpRead)
			if got := errors.Is(err, ErrDeniedPath); got != tc.deny {
				t.Fatalf("Check(%q) err = %v, deny = %v, want %v", tc.path, err, got, tc.deny)
			}
		})
	}
}

func TestReadOnlyExtra(t *testing.T) {
	t.Parallel()
	blob := realTemp(t)
	mkfile(t, filepath.Join(blob, "b1"), "blob")
	sb, err := New(&Config{Roots: []string{realTemp(t)}, ReadOnlyExtra: []string{blob}, Home: blob})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if v, _, err := sb.Check(t.Context(), filepath.Join(blob, "b1"), OpRead); v != Inside || err != nil {
		t.Fatalf("read blob: %v, %v", v, err)
	}
	if v, _, err := sb.Check(t.Context(), filepath.Join(blob, "b1"), OpWrite); v != OutsideRoot || err != nil {
		t.Fatalf("write blob: %v, %v", v, err)
	}
}

func TestNewAndCheckErrors(t *testing.T) {
	t.Parallel()
	dir := realTemp(t)
	mkfile(t, filepath.Join(dir, "file"), "x")
	for _, tc := range []struct {
		name string
		cfg  Config
		want error
	}{
		{"no root", Config{}, ErrNoRoot},
		{"missing root", Config{Roots: []string{filepath.Join(dir, "nope")}}, fs.ErrNotExist},
		{"root is file", Config{Roots: []string{filepath.Join(dir, "file")}}, fs.ErrInvalid},
		{"empty root", Config{Roots: []string{""}}, ErrInvalidPath},
		{"bad pattern", Config{Roots: []string{dir}, DenyPaths: []string{"/a/[x"}}, path.ErrBadPattern},
	} {
		if _, err := New(&tc.cfg); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}

	sb, err := New(&Config{Roots: []string{dir}, DenyPaths: []string{"~/.ssh"}})
	if err != nil {
		t.Fatalf("New with os home: %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context //nolint:containedctx // table input.
		path string
		want error
	}{
		{"canceled", canceled, dir, context.Canceled},
		{"empty", t.Context(), "", ErrInvalidPath},
		{"nul", t.Context(), "a\x00b", ErrInvalidPath},
	} {
		if _, _, err := sb.Check(tc.ctx, tc.path, OpRead); err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if v := Inside.String() + "," + OutsideRoot.String(); v != "inside,outside_root" {
		t.Errorf("String = %q", v)
	}
}

func TestRelativePathUsesCwd(t *testing.T) {
	t.Parallel()
	sb, err := New(&Config{Roots: []string{"."}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if v, resolved, err := sb.Check(t.Context(), "sandbox.go", OpRead); v != Inside || err != nil || !strings.HasSuffix(resolved, "sandbox.go") {
		t.Fatalf("Check = %v, %q, %v", v, resolved, err)
	}
}

func TestEdgeCases(t *testing.T) {
	t.Parallel()
	t.Run("EC-WIN-010", edgeLongPath)
	t.Run("EC-WIN-012", edgeMSYS)
	t.Run("EC-WIN-013", edgePathIdentity)
}

// edgeLongPath: paths > 260 characters (long-path prefix) work.
func edgeLongPath(t *testing.T) {
	t.Parallel()
	long := longPath()
	if got := normalizeWindows(`\\?\C:\` + long); got != `C:\`+long {
		t.Fatalf("normalize long = %q", got)
	}
	if !sameWindowsPath(`\\?\C:\`+long, `c:/`+strings.ReplaceAll(long, `\`, "/")) {
		t.Fatal("long-path prefix breaks identity")
	}
	// A real path > 260 characters beneath a root resolves Inside; the
	// I/O half of EC-WIN-010 is TestOpenEdgeCases.
	f := newFixture(t)
	p := longPathFile(t, f.root)
	if v, _, err := f.sb.Check(t.Context(), p, OpWrite); err != nil || v != Inside {
		t.Fatalf("check long: %v, %v", v, err)
	}
}

// longPath is a > 260-character relative path in Windows spelling.
func longPath() string { return strings.Repeat(`segment-0123456789\`, 16) + "f.txt" }

// longPathFile creates the parent directories of longPath beneath root and
// returns the file path (not created).
func longPathFile(t *testing.T, root string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(strings.ReplaceAll(longPath(), `\`, "/")))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return p
}

// edgeMSYS: Git Bash/MSYS paths normalize to Windows paths.
func edgeMSYS(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"/c/Users/x":       `C:\Users\x`,
		"/d":               `D:\`,
		"/cygdrive/e/a/b":  `E:\a\b`,
		"/tmp/x":           `\tmp\x`,
		"/cc/x":            `\cc\x`,
		`\??\C:\w`:         `C:\w`,
		`\\?\UNC\srv\sh\a`: `\\srv\sh\a`,
		`\\?\Volume{1}\a`:  `\\?\Volume{1}\a`,
	} {
		if got := normalizeWindows(in); got != want {
			t.Errorf("normalizeWindows(%q) = %q, want %q", in, got, want)
		}
	}
}

// edgePathIdentity: path equality ignores case and separator spelling.
func edgePathIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{`C:\Users\X\Proj\`, "c:/users/x/proj", true},
		{`C:\`, "c:", true},
		{`\\?\UNC\SRV\share\a`, `\\srv\share\a\.`, true},
		{"/c/Users/x", `C:\USERS\X`, true},
		{`C:\Users\x`, `C:\Users\xy`, false},
		{`\\srv\share`, `\srv\share`, false},
	} {
		if got := sameWindowsPath(tc.a, tc.b); got != tc.same {
			t.Errorf("sameWindowsPath(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
	root := filepath.Join("C:", "Users", "X", "Proj")
	if !hasPathPrefix(filepath.Join("c:", "users", "x", "proj", "a"), root, true) ||
		hasPathPrefix(root+"2", root, true) || hasPathPrefix(strings.ToLower(root), root, false) {
		t.Error("folded root prefix")
	}
}
