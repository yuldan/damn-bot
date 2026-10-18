package sandbox

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// modes runs fn with the primary open mechanism and the Linux < 5.6
// fallback (identical elsewhere).
func modes(t *testing.T, sb *Sandbox, fn func(t *testing.T, sb *Sandbox)) {
	t.Helper()
	fallback := *sb
	fallback.noOpenat2 = true
	for name, s := range map[string]*Sandbox{"primary": sb, "fallback": &fallback} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fn(t, s)
		})
	}
}

func TestOpenFile(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	symlink(t, "real.txt", filepath.Join(f.root, "link_inside"))
	symlink(t, filepath.Join(f.out, "outdir"), filepath.Join(f.root, "link_dir"))
	modes(t, f.sb, func(t *testing.T, sb *Sandbox) {
		openRoundTrip(t, sb, f.root)
		// A symlink present at open time but absent at Check time (the
		// TOCTOU window) is refused, also when it points inside the root.
		for _, rel := range []string{"link_inside", filepath.Join("link_dir", "f.txt")} {
			if _, err := sb.openBeneath(f.root, rel, filepath.Join(f.root, rel), os.O_RDONLY, 0); !errors.Is(err, ErrOutsideWorkspace) {
				t.Errorf("openBeneath(%q) = %v, want ErrOutsideWorkspace", rel, err)
			}
		}
		if _, err := sb.openBeneath(filepath.Join(f.root, "missing"), "x", "x", os.O_RDONLY, 0); err == nil {
			t.Error("openBeneath on missing root succeeded")
		}
	})
}

// openRoundTrip creates, reads back and misses files beneath root.
func openRoundTrip(t *testing.T, sb *Sandbox, root string) {
	t.Helper()
	dir := filepath.Join(root, "w-"+filepath.Base(t.Name()))
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	w, v, err := sb.OpenFile(t.Context(), filepath.Join(dir, "new.txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil || v != Inside {
		t.Fatalf("create: %v, %v", v, err)
	}
	if _, werr := w.WriteString("hello"); werr != nil {
		t.Fatalf("Write: %v", werr)
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	r, _, err := sb.OpenFile(t.Context(), filepath.Join(dir, "new.txt"), os.O_RDONLY, 0)
	if err != nil || readAll(t, r) != "hello" {
		t.Fatalf("read back: %v", err)
	}
	d, _, err := sb.OpenFile(t.Context(), root, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	if cerr := d.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	if _, _, err := sb.OpenFile(t.Context(), filepath.Join(dir, "missing"), os.O_RDONLY, 0); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
}

func TestOpenApprovedVolumeRoot(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	vol := filepath.VolumeName(f.root) + string(filepath.Separator)
	d, err := f.sb.OpenApproved(t.Context(), vol, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenApproved(%q): %v", vol, err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// flipEnv names the directory whose "d" entry the helper process flips.
const flipEnv = "DAMNBOT_SANDBOX_FLIP_DIR"

// TestSymlinkFlipHelper is not a test: TestSymlinkEscapeRace re-executes
// the test binary with flipEnv set to swap root/d between a real directory
// and a symlink to an outside directory until killed (no goroutines, E-26).
// In a normal run flipEnv is unset and it returns at once (passes, no skip).
func TestSymlinkFlipHelper(t *testing.T) {
	dir := os.Getenv(flipEnv)
	if dir == "" {
		return
	}
	d, realDir, link := filepath.Join(dir, "d"), filepath.Join(dir, "d.real"), filepath.Join(dir, "d.link")
	for range 1 << 24 { // bounded; the parent kills us far earlier
		for _, mv := range [][2]string{{d, realDir}, {link, d}, {d, link}, {realDir, d}} {
			if err := os.Rename(mv[0], mv[1]); err != nil { //nolint:gosec // test-owned temp dir.
				t.Fatalf("Rename: %v", err)
			}
		}
	}
}

// TestSymlinkEscapeRace is CT-26: while another process swaps a path
// component between an inside directory and a symlink to an outside one,
// 1000 opens per mechanism never yield the outside file.
func TestSymlinkEscapeRace(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mkfile(t, filepath.Join(f.root, "d", "f.txt"), "inside")
	symlink(t, filepath.Join(f.out, "outdir"), filepath.Join(f.root, "d.link"))
	inside, err := os.Stat(filepath.Join(f.root, "d", "f.txt"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	outside, err := os.Stat(filepath.Join(f.out, "outdir", "f.txt"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	startFlipper(t, f.root)
	modes(t, f.sb, func(t *testing.T, sb *Sandbox) {
		var opened, refused int
		for i := range 1000 {
			flag := os.O_RDONLY
			if i%2 == 1 {
				flag = os.O_RDWR
			}
			for _, file := range raceOpen(t, sb, f.root, flag) {
				if file.f == nil {
					if errors.Is(file.err, ErrOutsideWorkspace) {
						refused++
					}
					continue
				}
				opened++
				assertSame(t, file.f, inside, outside)
			}
		}
		t.Logf("opened %d, refused as escape %d", opened, refused)
	})
}

type openResult struct {
	f   *os.File
	err error
}

// raceOpen opens root/d/f.txt through the public API (Check window
// included) and through the open mechanism alone.
func raceOpen(t *testing.T, sb *Sandbox, root string, flag int) [2]openResult {
	t.Helper()
	p := filepath.Join(root, "d", "f.txt")
	var res [2]openResult
	res[0].f, _, res[0].err = sb.OpenFile(t.Context(), p, flag, 0)
	res[1].f, res[1].err = sb.openBeneath(root, filepath.Join("d", "f.txt"), p, flag, 0)
	return res
}

// startFlipper re-executes the test binary as the flipping process and
// kills and reaps it at cleanup.
func startFlipper(t *testing.T, root string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSymlinkFlipHelper$") //nolint:gosec // re-exec of the test binary.
	cmd.Env = append(os.Environ(), flipEnv+"="+root)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start flipper: %v", err)
	}
	t.Cleanup(func() {
		// Kill can fail when the flipper already exited (Windows reports
		// "Access is denied" for a finished process); Wait settles it.
		kerr := cmd.Process.Kill()
		var exit *exec.ExitError
		if err := cmd.Wait(); err != nil && !errors.As(err, &exit) {
			t.Errorf("Wait: %v (Kill: %v)", err, kerr)
		}
	})
}

func assertSame(t *testing.T, file *os.File, inside, outside fs.FileInfo) {
	t.Helper()
	fi, err := file.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if cerr := file.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	if os.SameFile(fi, outside) || !os.SameFile(fi, inside) {
		t.Fatalf("opened a file outside the root: %v", fi)
	}
}

// TestOpenOutsideRootVerdict: an outside symlink is a verdict for OpenFile,
// OpenApproved then opens it; deny_paths wins over both.
func TestOpenOutsideRootVerdict(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	link, deny := filepath.Join(f.root, "link_file"), filepath.Join(f.root, "link_deny")
	symlink(t, filepath.Join(f.out, "secret.txt"), link)
	symlink(t, filepath.Join(f.home, ".ssh", "id_rsa"), deny)
	file, v, err := f.sb.OpenFile(t.Context(), link, os.O_RDONLY, 0)
	if file != nil || v != OutsideRoot || err != nil {
		t.Fatalf("OpenFile = %v, %v, %v; want nil, outside_root, nil", file, v, err)
	}
	approved, err := f.sb.OpenApproved(t.Context(), link, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenApproved: %v", err)
	}
	if got := readAll(t, approved); got != "outside" {
		t.Fatalf("approved read %q", got)
	}
	if _, err := f.sb.OpenApproved(t.Context(), deny, os.O_RDONLY, 0); !errors.Is(err, ErrDeniedPath) {
		t.Fatalf("OpenApproved deny: %v", err)
	}
	if _, _, err := f.sb.OpenFile(t.Context(), deny, os.O_RDONLY, 0); !errors.Is(err, ErrDeniedPath) {
		t.Fatalf("OpenFile deny: %v", err)
	}
}

// TestOpenDeniedWrite: deny_paths wins over the root for a create, and
// nothing is created.
func TestOpenDeniedWrite(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, _, err := f.sb.OpenFile(t.Context(), filepath.Join(f.root, ".env"), os.O_CREATE|os.O_WRONLY, 0o600)
	if !errors.Is(err, ErrDeniedPath) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".env")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf(".env created: %v", err)
	}
}

// TestOpenReadOnlyExtra: a ReadOnlyExtra file opens for reading.
func TestOpenReadOnlyExtra(t *testing.T) {
	t.Parallel()
	blob := realTemp(t)
	mkfile(t, filepath.Join(blob, "b1"), "blob")
	sb, err := New(&Config{Roots: []string{realTemp(t)}, ReadOnlyExtra: []string{blob}, Home: blob})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	file, v, err := sb.OpenFile(t.Context(), filepath.Join(blob, "b1"), os.O_RDONLY, 0)
	if err != nil || v != Inside {
		t.Fatalf("read blob: %v, %v", v, err)
	}
	if got := readAll(t, file); got != "blob" {
		t.Fatalf("read %q", got)
	}
}

// TestOpenFileErrors: Check errors surface from OpenFile and OpenApproved.
func TestOpenFileErrors(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context //nolint:containedctx // table input.
		path string
		want error
	}{
		{"canceled", canceled, f.root, context.Canceled},
		{"empty", t.Context(), "", ErrInvalidPath},
		{"nul", t.Context(), "a\x00b", ErrInvalidPath},
		{"not a dir", t.Context(), filepath.Join(f.root, "real.txt", "x"), nil},
	} {
		if _, _, err := f.sb.OpenFile(tc.ctx, tc.path, os.O_RDONLY, 0); err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("OpenFile %s: err = %v, want %v", tc.name, err, tc.want)
		}
		if _, err := f.sb.OpenApproved(tc.ctx, tc.path, os.O_RDONLY, 0); err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("OpenApproved %s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// TestOpenEdgeCases/EC-WIN-010: real I/O beneath a root through a path
// longer than 260 characters.
func TestOpenEdgeCases(t *testing.T) {
	t.Parallel()
	t.Run("EC-WIN-010", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		w, v, err := f.sb.OpenFile(t.Context(), longPathFile(t, f.root), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil || v != Inside {
			t.Fatalf("create long: %v, %v", v, err)
		}
		if cerr := w.Close(); cerr != nil {
			t.Fatalf("Close: %v", cerr)
		}
	})
}

func readAll(t *testing.T, f *os.File) string {
	t.Helper()
	b := make([]byte, 64)
	n, err := f.Read(b)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return string(b[:n])
}

func BenchmarkOpenFile(b *testing.B) {
	root := b.TempDir()
	p := filepath.Join(root, "a", "b", "f.txt")
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		b.Fatal(err)
	}
	sb, err := New(&Config{Roots: []string{root}, DenyPaths: []string{"~/.ssh", "**/.env", "**/*.pem", "/proc/*/environ"}, Home: root})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		f, _, err := sb.OpenFile(b.Context(), p, os.O_RDONLY, 0)
		if err != nil {
			b.Fatal(err)
		}
		if err := f.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
