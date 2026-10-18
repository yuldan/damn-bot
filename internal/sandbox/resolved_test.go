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
		filepath.Join(f.root, "x", "y.Pem"):                  true,
		filepath.Join(f.home, ".Ssh", "sub", "deep"):         true,
		filepath.Join(f.home, "ssh", "id_rsa"):               false,
		filepath.FromSlash("/proc/42/mem"):                   true,
		filepath.FromSlash("/proc/42/memx"):                  false,
		f.root:                                               false,
	} {
		if got := f.sb.Denied(p); got != want {
			t.Errorf("Denied(%q) = %v, want %v", p, got, want)
		}
	}
	// A literal file pattern denies itself (any case) and anything below
	// it, not its siblings or a name it prefixes.
	token := filepath.Join(f.out, "token")
	sb, err := New(&Config{Roots: []string{f.root}, DenyPaths: []string{token}, Home: f.home})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for p, want := range map[string]bool{
		token: true, filepath.Join(f.out, "TOKEN"): true, filepath.Join(token, "x"): true,
		token + "2": false, filepath.Join(f.out, "tok"): false, filepath.Join(f.root, "token"): false,
	} {
		if got := sb.Denied(p); got != want {
			t.Errorf("literal: Denied(%q) = %v, want %v", p, got, want)
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

// TestOpenResolvedHardLinks (S16-2, S16-7, CT27-6): hard links inside the
// root to a file a literal deny pattern names are refused after the open,
// approved or not, and so is one outside every root on the approved
// branch; a hard link to a file in a deny dir (~/.ssh) or to a "**/.env"
// match, invisible to every path check, is HardLinked unless approved,
// checked on the handle; a singly linked file opens.
func TestOpenResolvedHardLinks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	secret := filepath.Join(f.out, "token.txt")
	mkfile(t, secret, "tok")
	mkfile(t, filepath.Join(f.out, "proj", ".env"), "TOKEN=v")
	sb, err := New(&Config{Roots: []string{f.root}, DenyPaths: []string{secret, "~/.ssh", "**/.env"}, Home: f.home})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	in := func(name string) string { return filepath.Join(f.root, name) }
	for link, old := range map[string]string{
		in("alias.txt"):                    secret,
		filepath.Join(f.out, "alias2.txt"): secret,
		in("linked.txt"):                   filepath.Join(f.out, "secret.txt"),
		in("key"):                          filepath.Join(f.home, ".ssh", "id_rsa"),
		in("env"):                          filepath.Join(f.out, "proj", ".env"),
	} {
		if err := os.Link(old, link); err != nil {
			t.Fatalf("Link: %v", err)
		}
	}
	modes(t, sb, func(t *testing.T, sb *Sandbox) {
		ctx := t.Context()
		for _, p := range []string{in("alias.txt"), filepath.Join(f.out, "alias2.txt")} {
			for _, approved := range []bool{false, true} {
				if p == filepath.Join(f.out, "alias2.txt") && !approved {
					continue // outside_root: refused before any open
				}
				if file, _, err := sb.OpenResolved(ctx, p, approved); file != nil || !errors.Is(err, ErrDeniedPath) {
					t.Errorf("OpenResolved(%s, approved %v) = %v; want ErrDeniedPath", p, approved, err)
				}
			}
		}
		for _, name := range []string{"linked.txt", "key", "env"} {
			if file, v, err := sb.OpenResolved(ctx, in(name), false); file != nil || err != nil || v != HardLinked {
				t.Errorf("OpenResolved(%s, unapproved) = %v, %v; want hardlinked_file", name, v, err)
			}
		}
		file, v, err := sb.OpenResolved(ctx, in("linked.txt"), true)
		if err != nil || v != Inside {
			t.Fatalf("OpenResolved(hard link, approved) = %v, %v", v, err)
		}
		closeT(t, file)
		if file, v, err = sb.OpenResolved(ctx, in("real.txt"), false); err != nil || v != Inside {
			t.Fatalf("OpenResolved(nlink 1) = %v, %v", v, err)
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
