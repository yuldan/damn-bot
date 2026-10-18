//go:build unix

package codetools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
	"golang.org/x/sys/unix"
)

// grepFiles lists the files of a files-mode grep (or fails the test).
func grepFiles(t *testing.T, v *env, a *GrepArgs) []string {
	t.Helper()
	a.Mode = "files"
	var got []string
	for _, l := range strings.Split(grep(t, v, a).Content, "\n")[1:] {
		if p, _, ok := strings.Cut(l, " #"); ok {
			got = append(got, p)
		}
	}
	return got
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
}

// TestIgnoreSymlinkOutsideRoot (CT27-3): a symlinked .gitignore/.ignore is
// absent, like git ≥ 2.32 (ELOOP), whether it points outside the root or
// inside it, both for the walker and for the ancestors of a search path.
func TestIgnoreSymlinkOutsideRoot(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	private := filepath.Join(v.out, "private.txt")
	writeFile(t, private, "secretname.go\n")
	v.put(t, "secretname.go", "needle\n")
	v.put(t, "sub/deep/secretname.go", "needle\n")
	v.put(t, "inner.txt", "secretname.go\n")
	symlink(t, private, filepath.Join(v.root, ".gitignore"))
	symlink(t, private, filepath.Join(v.root, "sub", ".ignore"))
	symlink(t, filepath.Join(v.root, "inner.txt"), filepath.Join(v.root, "sub", "deep", ".gitignore"))
	if got := grepFiles(t, v, &GrepArgs{Pattern: "needle"}); !slices.Equal(got, []string{"secretname.go", "sub/deep/secretname.go"}) {
		t.Errorf("grep: %v", got)
	}
	if got := grepFiles(t, v, &GrepArgs{Pattern: "needle", Path: "sub/deep"}); !slices.Equal(got, []string{"sub/deep/secretname.go"}) {
		t.Errorf("grep sub/deep (ancestor ignore files): %v", got)
	}
	r, err := v.e.Glob(context.Background(), GlobArgs{Pattern: "**/secretname.go"}, Opts{})
	if err != nil || r.Meta["count"] != 2 {
		t.Errorf("glob: %v %q", err, r.Content)
	}
}

// TestRepoExcludesFileOutsideRoot (CT27-3): core.excludesFile and
// include.path of the repository's .git/config apply only Inside the
// root; the global configuration may live outside it (ADR-103 butir 4).
func TestRepoExcludesFileOutsideRoot(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	for _, f := range []string{"secretname.go", "other.go", "global.go"} {
		v.put(t, f, "needle\n")
	}
	in := v.put(t, "in.ignore", "other.go\n")
	outX, outCfg := filepath.Join(v.out, "x"), filepath.Join(v.out, "cfg")
	writeFile(t, outX, "secretname.go\n")
	writeFile(t, outCfg, "[core]\n\texcludesFile = "+in+"\n") // would hide other.go
	v.put(t, ".git/config", "[core]\n\texcludesFile = "+outX+"\n[include]\n\tpath = "+outCfg+"\n")
	writeFile(t, filepath.Join(v.home, ".config", "git", "ignore"), "global.go\n") // overridden by the repo
	if got := grepFiles(t, v, &GrepArgs{Pattern: "needle"}); !slices.Equal(got, []string{"global.go", "other.go", "secretname.go"}) {
		t.Errorf("outside repo excludes applied: %v", got)
	}
	// Control: the same configuration Inside the root applies.
	v.put(t, ".git/config", "[core]\n\texcludesFile = "+in+"\n")
	if got := grepFiles(t, v, &GrepArgs{Pattern: "needle"}); !slices.Equal(got, []string{"global.go", "secretname.go"}) {
		t.Errorf("inside repo excludes: %v", got)
	}
	// Global: ~/.gitconfig and its include.path, outside the root.
	writeFile(t, filepath.Join(v.home, ".gitconfig"), "[include]\n\tpath = "+outCfg+"\n")
	writeFile(t, outCfg, "[core]\n\texcludesFile = "+outX+"\n")
	v.put(t, ".git/config", "")
	if got := grepFiles(t, v, &GrepArgs{Pattern: "needle"}); !slices.Equal(got, []string{"global.go", "other.go"}) {
		t.Errorf("global excludes: %v", got)
	}
}

// TestGrepApprovedWalkRace (CT27-1, CT27-6): while a goroutine swaps a
// walked subdirectory with a symlink to a deny_paths directory, no grep
// output ever contains the key, for an approved walk outside the root and
// for a walk inside it.
func TestGrepApprovedWalkRace(t *testing.T) {
	t.Parallel()
	for _, outside := range []bool{true, false} {
		v := newEnvDeny(t, "~/.ssh/**")
		ssh := filepath.Join(v.home, ".ssh")
		writeFile(t, filepath.Join(ssh, "id_key"), "KEYMATERIAL\n")
		tree, path, o := filepath.Join(v.root, "tree"), "tree", Opts{}
		if outside {
			tree, o = filepath.Join(v.out, "tree"), Opts{Approved: true}
			path = tree
		}
		sub := filepath.Join(tree, "sub")
		writeFile(t, filepath.Join(sub, "f.txt"), "safe\n")
		var stop atomic.Bool
		var wg sync.WaitGroup
		wg.Go(func() {
			for !stop.Load() {
				if err := swapSymlink(sub, ssh); err != nil {
					t.Errorf("swap: %v", err)
					return
				}
			}
		})
		seen, leakAt, leak := raceGreps(v, path, o)
		stop.Store(true)
		wg.Wait()
		if leak != "" {
			t.Fatalf("outside=%v iteration %d leaked the key:\n%s", outside, leakAt, leak)
		}
		if seen == 0 {
			t.Errorf("outside=%v: no grep saw the real file (vacuous)", outside)
		}
	}
}

// raceGreps runs up to 300 greps of path: seen counts those that saw the
// real file; the first output holding the key stops it (leak, at leakAt).
func raceGreps(v *env, path string, o Opts) (seen, leakAt int, leak string) {
	for i := range 300 {
		r, err := v.e.Grep(context.Background(), &GrepArgs{Pattern: "K.YMATERIAL|safe", Path: path}, o)
		if err != nil {
			continue // the search root itself may be mid-swap
		}
		if strings.Contains(r.Content, "KEYMATERIAL") {
			return seen, i, r.Content
		}
		if strings.Contains(r.Content, "safe") {
			seen++
		}
	}
	return seen, 0, ""
}

// swapSymlink replaces dir by a symlink to target, yields, and restores
// it (one TestGrepApprovedWalkRace swap).
func swapSymlink(dir, target string) error {
	if err := os.Rename(dir, dir+".real"); err != nil {
		return fmt.Errorf("hide: %w", err)
	}
	if err := os.Symlink(target, dir); err != nil {
		return fmt.Errorf("symlink: %w", err)
	}
	runtime.Gosched()
	if err := os.Remove(dir); err != nil {
		return fmt.Errorf("unlink: %w", err)
	}
	if err := os.Rename(dir+".real", dir); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	return nil
}

// TestGrepSkipsHardLinked (CT27-13): a hard link inside the root to a file
// in a deny_paths directory is never read by grep, approved or not, and
// the footer counts it.
func TestGrepSkipsHardLinked(t *testing.T) {
	t.Parallel()
	v := newEnvDeny(t, "~/.ssh/**")
	key := filepath.Join(v.home, ".ssh", "id_key")
	writeFile(t, key, "KEYMATERIAL\n")
	v.put(t, "a.txt", "needle\n")
	if err := os.Link(key, filepath.Join(v.root, "linked.txt")); err != nil {
		t.Fatalf("Link: %v", err)
	}
	for _, o := range []Opts{{}, {Approved: true}} {
		r, err := v.e.Grep(context.Background(), &GrepArgs{Pattern: "K.YMATERIAL|needle"}, o)
		if err != nil || strings.Contains(r.Content, "KEYMATERIAL") || r.Meta["files"] != 1 || !strings.Contains(r.Content, "skipped: 1 hard-linked") {
			t.Errorf("approved=%v: %v\n%s", o.Approved, err, r.Content)
		}
	}
}

// TestFIFONoHang (CT27-4): FIFOs as files and as ignore files never block
// grep, glob, read_file or edit_file past their deadline, and leak no
// goroutine.
func TestFIFONoHang(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	v := newEnv(t)
	v.put(t, "a.txt", "needle\n")
	v.put(t, "sub/deep/b.txt", "needle\n")
	fifos := []string{filepath.Join(v.root, "pipe"), filepath.Join(v.root, "sub", ".gitignore")}
	edit := func(ctx context.Context) error {
		s := "x"
		_, err := v.e.Edit(ctx, EditArgs{Path: "pipe", Tag: "00000000", Edits: []EditItem{{OldString: &s, NewString: &s}}}, Opts{})
		return err
	}
	grepIn := func(path string) func(context.Context) error {
		return func(ctx context.Context) error {
			r, err := v.e.Grep(ctx, &GrepArgs{Pattern: "needle", Path: path}, Opts{})
			if err == nil && path != "pipe" && r.Meta["files"] == 0 {
				return errors.New("no match: " + r.Content)
			}
			return err
		}
	}
	ops := []struct {
		name string
		run  func(context.Context) error
		want Code // "" = success or any tool error
	}{
		{"read_file", func(ctx context.Context) error {
			_, err := v.e.Read(ctx, ReadArgs{Path: "pipe"}, Opts{})
			return err
		}, CodeInvalidArgs},
		{"edit_file", edit, CodeInvalidArgs},
		{"grep pipe", grepIn("pipe"), CodeInvalidArgs},
		{"grep", grepIn("."), ""},
		{"grep sub/deep", grepIn("sub/deep"), ""},
		{"glob", func(ctx context.Context) error {
			_, err := v.e.Glob(ctx, GlobArgs{Pattern: "**"}, Opts{})
			return err
		}, ""},
	}
	for _, op := range ops {
		remakeFIFOs(t, fifos) // fresh FIFOs: no writer state left by a release
		err := runUnblocked(t, op.name, op.run, fifos)
		if got := codeOf(err); got != op.want && (op.want != "" || got == "" && err != nil) {
			t.Errorf("%s: code %q, want %q (err %v)", op.name, got, op.want, err)
		}
	}
}

// remakeFIFOs replaces every path in fifos by a new FIFO.
func remakeFIFOs(t *testing.T, fifos []string) {
	t.Helper()
	for _, f := range fifos {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(f, 0o600); err != nil {
			t.Fatalf("Mkfifo: %v", err)
		}
	}
}

// runUnblocked runs op with a 2 s deadline; an op still blocked 0.5 s past
// it fails the test and is released by opening a writer on each FIFO.
func runUnblocked(t *testing.T, name string, op func(context.Context) error, fifos []string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- op(ctx) }()
	select {
	case err := <-done:
		return err
	case <-time.After(2500 * time.Millisecond):
		t.Errorf("%s still blocked after %v", name, time.Since(start))
		for _, f := range fifos { // a writer releases a blocked open
			if w, werr := os.OpenFile(filepath.Clean(f), os.O_RDWR, 0); werr == nil {
				if cerr := w.Close(); cerr != nil {
					t.Error(cerr)
				}
			}
		}
		return <-done
	}
}
