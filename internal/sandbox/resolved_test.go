package sandbox

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestDenied covers the compiled deny matchers (ADR-103): literal and
// "*suffix" fast paths fold ASCII case in place, other components are
// lower-cased like the patterns, globs fall back to path.Match, and a
// match on an ancestor denies everything beneath it. Not parallel:
// AllocsPerRun.
func TestDenied(t *testing.T) {
	f := newFixture(t)
	for p, want := range map[string]bool{
		filepath.Join(f.root, ".env"):                        true,
		filepath.Join(f.root, "a", ".ENV"):                   true,
		filepath.Join(f.root, "a", ".env", "nested"):         true,
		filepath.Join(f.root, "KEY.PEM"):                     true,
		filepath.Join(f.root, "Ä.PEM"):                       true,
		filepath.Join(f.root, "pem"):                         false,
		filepath.Join(f.root, "x.pem.txt"):                   false,
		filepath.Join(f.root, "env"):                         false,
		filepath.Join(f.root, "É", "ok.txt"):                 false,
		filepath.Join(f.home, ".ssh", "id_rsa"):              true,
		filepath.Join(f.home, ".SSH"):                        true,
		filepath.Join(f.home, ".sshx"):                       false,
		filepath.FromSlash("/proc/42/environ"):               true,
		filepath.FromSlash("/proc/42/task/7/environ"):        true,
		filepath.FromSlash("/proc/42/status"):                false,
		filepath.Join(f.root, filepath.FromSlash("a/b/c/d")): false,
	} {
		if got := f.sb.Denied(p); got != want {
			t.Errorf("Denied(%q) = %v, want %v", p, got, want)
		}
	}
	p := filepath.Join(f.root, "Sub", "Dir", "file.go")
	if n := testing.AllocsPerRun(100, func() { f.sb.Denied(p) }); n != 0 {
		t.Errorf("Denied allocates %.0f times per ASCII path, want 0", n)
	}
}

// TestOpenResolved: walker opens refuse every symlink on the path, enforce
// deny_paths before and after the open, and return OutsideRoot (approval)
// outside roots.
func TestOpenResolved(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mkfile(t, filepath.Join(f.root, "sub", ".env"), "secret")
	symlink(t, "real.txt", filepath.Join(f.root, "link_inside"))
	symlink(t, f.out, filepath.Join(f.root, "link_out"))
	modes(t, f.sb, func(t *testing.T, sb *Sandbox) {
		ctx := t.Context()
		for _, rel := range []string{"real.txt", "sub", "."} {
			file, v, err := sb.OpenResolved(ctx, filepath.Join(f.root, rel), false)
			if err != nil || v != Inside {
				t.Fatalf("OpenResolved(%s) = %v, %v", rel, v, err)
			}
			closeT(t, file)
		}
		for rel, want := range map[string]error{
			filepath.Join("sub", ".env"):            ErrDeniedPath,
			"link_inside":                           ErrOutsideWorkspace,
			filepath.Join("link_out", "secret.txt"): ErrOutsideWorkspace,
		} {
			if _, _, err := sb.OpenResolved(ctx, filepath.Join(f.root, rel), false); !errors.Is(err, want) {
				t.Errorf("OpenResolved(%s) = %v, want %v", rel, err, want)
			}
		}
		outside := filepath.Join(f.out, "secret.txt")
		if file, v, err := sb.OpenResolved(ctx, outside, false); file != nil || err != nil || v != OutsideRoot {
			t.Errorf("OpenResolved(outside, unapproved) = %v, %v; want outside_root", v, err)
		}
		file, v, err := sb.OpenResolved(ctx, outside, true)
		if err != nil || v != OutsideRoot {
			t.Fatalf("OpenResolved(outside, approved) = %v, %v", v, err)
		}
		if b, rerr := io.ReadAll(file); rerr != nil || string(b) != "outside" {
			t.Errorf("approved read = %q, %v", b, rerr)
		}
		closeT(t, file)
		if _, _, err := sb.OpenResolved(ctx, "rel.txt", false); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("OpenResolved(relative) = %v, want ErrInvalidPath", err)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, _, err := sb.OpenResolved(canceled, filepath.Join(f.root, "real.txt"), false); !errors.Is(err, context.Canceled) {
			t.Errorf("OpenResolved(canceled) = %v, want context.Canceled", err)
		}
	})
}

// TestOpenResolvedHardLinks: a hard link inside the root to a file a
// literal deny pattern names is refused after the open (S16-2), even when
// approved; any other multiply linked file is HardLinked unless approved
// (S16-7), checked on the handle.
func TestOpenResolvedHardLinks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	secret := filepath.Join(f.out, "token.txt")
	mkfile(t, secret, "tok")
	sb, err := New(&Config{Roots: []string{f.root}, DenyPaths: []string{secret}, Home: f.home})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	alias, linked := filepath.Join(f.root, "alias.txt"), filepath.Join(f.root, "linked.txt")
	for old, link := range map[string]string{secret: alias, filepath.Join(f.out, "secret.txt"): linked} {
		if err := os.Link(old, link); err != nil {
			t.Fatalf("Link: %v", err)
		}
	}
	modes(t, sb, func(t *testing.T, sb *Sandbox) {
		ctx := t.Context()
		if _, _, err := sb.OpenResolved(ctx, alias, true); !errors.Is(err, ErrDeniedPath) {
			t.Errorf("OpenResolved(hard link to deny target) = %v, want ErrDeniedPath", err)
		}
		if file, v, err := sb.OpenResolved(ctx, linked, false); file != nil || err != nil || v != HardLinked {
			t.Errorf("OpenResolved(hard link, unapproved) = %v, %v; want hardlinked_file", v, err)
		}
		file, v, err := sb.OpenResolved(ctx, linked, true)
		if err != nil || v != Inside {
			t.Fatalf("OpenResolved(hard link, approved) = %v, %v", v, err)
		}
		closeT(t, file)
	})
}

func closeT(t *testing.T, f *os.File) {
	t.Helper()
	if err := f.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
