package journal

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/sandbox"
)

// swapDir alternates dir between a real directory and a symlink to target,
// moving the directory to parked each time; it runs until done.
func swapDir(dir, parked, target string, done <-chan struct{}, errc chan<- error) {
	symlinked := false
	for {
		select {
		case <-done:
			return
		default:
		}
		var err error
		if symlinked {
			err = unsymlinkDir(dir, parked)
		} else {
			err = symlinkDir(dir, parked, target)
		}
		if err != nil {
			select {
			case errc <- err:
			default:
			}
			return
		}
		symlinked = !symlinked
		runtime.Gosched()
	}
}

// symlinkDir moves dir to parked and leaves a symlink to target in its place.
//
//nolint:wrapcheck // the test surfaces the OS cause to the swapper.
func symlinkDir(dir, parked, target string) error {
	if err := os.Rename(dir, parked); err != nil {
		return err
	}
	if err := os.Symlink(target, dir); err != nil {
		return errors.Join(err, os.Rename(parked, dir))
	}
	return nil
}

// unsymlinkDir removes the symlink at dir and moves the parked directory back.
//
//nolint:wrapcheck // the test surfaces the OS cause to the swapper.
func unsymlinkDir(dir, parked string) error {
	if err := os.Remove(dir); err != nil {
		return err
	}
	return os.Rename(parked, dir)
}

// TestUndoAncestorSwapStaysInSandbox (REVIEW-F2-SEC5-12): swapping the
// action's parent directory for a symlink to an outside directory between
// the sandbox check and the write must never let the undo executor write
// outside the journaled root. Bounded by a deadline; the outside file is
// polled after every attempt.
func TestUndoAncestorSwapStaysInSandbox(t *testing.T) {
	root, u, f := workspace(t)
	inside := filepath.Join(root, "real")
	outside := t.TempDir()
	if err := os.Mkdir(inside, 0o750); err != nil {
		t.Fatal(err)
	}
	inner, err := os.OpenRoot(inside) // survives the renames of the swapper
	if err != nil {
		t.Fatal(err)
	}
	defer inner.Close() //nolint:errcheck // test cleanup
	reset := func() {
		w, oerr := inner.OpenFile("f.txt", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if oerr != nil {
			t.Fatal(oerr)
		}
		if _, werr := w.WriteString("new"); werr != nil {
			t.Fatal(werr)
		}
		if werr := w.Close(); werr != nil {
			t.Fatal(werr)
		}
	}
	reset()
	outsideFile := filepath.Join(outside, "f.txt")
	write(t, outsideFile, []byte("OUTSIDE"), 0o600)

	dir := filepath.Join(root, "sub")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	f.add(action(1, FileWrite, filepath.Join(dir, "f.txt"), f.put([]byte("old"), 0o600), f.put([]byte("new"), 0o600)))

	done := make(chan struct{})
	errc := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Go(func() { swapDir(dir, dir+".parked", outside, done, errc) })

	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		select {
		case err := <-errc:
			close(done)
			wg.Wait()
			t.Fatal(err)
		default:
		}
		u.Undo(t.Context(), "s1", 1, true) //nolint:errcheck,gosec // refusals are expected; the outside file is the assertion.
		if got := read(t, outsideFile); string(got) != "OUTSIDE" {
			close(done)
			wg.Wait()
			t.Fatalf("undo wrote outside the sandbox: %q", got)
		}
		reset()
	}
	close(done)
	wg.Wait()
}

// TestUndoActionsExecutesCheckedSet (REVIEW-F2-SEC5-14): undo N must undo
// exactly the actions the caller listed and checked, not a set re-listed
// after the lease (a newer action appended meanwhile stays untouched).
func TestUndoActionsExecutesCheckedSet(t *testing.T) {
	root, u, f := workspace(t)
	p := func(name string) string { return filepath.Join(root, name) }
	for _, name := range []string{"a", "b", "c"} {
		write(t, p(name), []byte("v2"), 0o600)
	}
	f.add(action(1, FileWrite, p("a"), f.put([]byte("v1"), 0o600), f.put([]byte("v2"), 0o600)))
	f.add(action(2, FileWrite, p("b"), f.put([]byte("v1"), 0o600), f.put([]byte("v2"), 0o600)))
	checked := []Action{f.acts[2], f.acts[1]}
	f.add(action(3, FileWrite, p("c"), f.put([]byte("v1"), 0o600), f.put([]byte("v2"), 0o600)))

	res, err := u.UndoActions(t.Context(), "s1", checked, false)
	if err != nil || len(res) != 2 {
		t.Fatalf("UndoActions = %d results, %v", len(res), err)
	}
	if got := string(read(t, p("a"))); got != "v1" {
		t.Fatalf("checked action a = %q, want v1", got)
	}
	if got := string(read(t, p("b"))); got != "v1" {
		t.Fatalf("checked action b = %q, want v1", got)
	}
	if got := string(read(t, p("c"))); got != "v2" {
		t.Fatalf("undo touched an action outside the checked set: c = %q", got)
	}
}

// deniedSandbox returns a workspace root whose .ssh directory is denied and
// an undoer over it.
func deniedSandbox(t *testing.T) (string, *Undoer, *fakeStore) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sb, err := sandbox.New(&sandbox.Config{
		Roots:     []string{root},
		DenyPaths: []string{filepath.Join(root, ".ssh")},
		Home:      root,
	})
	if err != nil {
		t.Fatal(err)
	}
	f := newFake()
	u, err := NewUndoer(f, sb)
	if err != nil {
		t.Fatal(err)
	}
	return root, u, f
}

// TestUndoRelativeSymlinkToDeniedDir (REVIEW-F2-SEC5-15): an ancestor
// swapped for a symlink to a deny_paths directory *inside* the root must
// not let undo write there.
func TestUndoRelativeSymlinkToDeniedDir(t *testing.T) {
	root, u, f := deniedSandbox(t)
	denied := filepath.Join(root, ".ssh")
	if err := os.Mkdir(denied, 0o750); err != nil {
		t.Fatal(err)
	}
	deniedFile := filepath.Join(denied, "f.txt")
	write(t, deniedFile, []byte("DENIED"), 0o600)
	dir := filepath.Join(root, "sub")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	inner, err := os.OpenRoot(dir) // survives the renames of the swapper
	if err != nil {
		t.Fatal(err)
	}
	defer inner.Close() //nolint:errcheck // test cleanup
	reset := func() {
		w, oerr := inner.OpenFile("f.txt", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if oerr != nil {
			t.Fatal(oerr)
		}
		if _, werr := w.WriteString("new"); werr != nil {
			t.Fatal(werr)
		}
		if werr := w.Close(); werr != nil {
			t.Fatal(werr)
		}
	}
	reset()
	f.add(action(1, FileWrite, filepath.Join(dir, "f.txt"), f.put([]byte("old"), 0o600), f.put([]byte("new"), 0o600)))

	done := make(chan struct{})
	errc := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Go(func() { swapDir(dir, dir+".parked", ".ssh", done, errc) })

	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		select {
		case err := <-errc:
			close(done)
			wg.Wait()
			t.Fatal(err)
		default:
		}
		u.Undo(t.Context(), "s1", 1, true) //nolint:errcheck,gosec // refusals are expected; the denied file is the assertion.
		if got := read(t, deniedFile); string(got) != "DENIED" {
			close(done)
			wg.Wait()
			t.Fatalf("undo wrote into a denied directory: %q", got)
		}
		reset()
	}
	close(done)
	wg.Wait()
}

// TestUndoRootReplacedBySymlink (REVIEW-F2-SEC5-16): replacing the sandbox
// root itself with a symlink after New must not redirect undo.
func TestUndoRootReplacedBySymlink(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	outside := filepath.Join(base, "other")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "f.txt")
	write(t, outsideFile, []byte("OUTSIDE"), 0o600)
	sb, err := sandbox.New(&sandbox.Config{Roots: []string{root}, Home: root})
	if err != nil {
		t.Fatal(err)
	}
	f := newFake()
	u, err := NewUndoer(f, sb)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer inner.Close() //nolint:errcheck // test cleanup
	reset := func() {
		w, oerr := inner.OpenFile("f.txt", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if oerr != nil {
			t.Fatal(oerr)
		}
		if _, werr := w.WriteString("new"); werr != nil {
			t.Fatal(werr)
		}
		if werr := w.Close(); werr != nil {
			t.Fatal(werr)
		}
	}
	reset()
	f.add(action(1, FileWrite, filepath.Join(root, "f.txt"), f.put([]byte("old"), 0o600), f.put([]byte("new"), 0o600)))

	done := make(chan struct{})
	errc := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Go(func() { swapDir(root, filepath.Join(base, "ws.parked"), outside, done, errc) })

	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		select {
		case err := <-errc:
			close(done)
			wg.Wait()
			t.Fatal(err)
		default:
		}
		u.Undo(t.Context(), "s1", 1, true) //nolint:errcheck,gosec // refusals are expected; the outside file is the assertion.
		if got := read(t, outsideFile); string(got) != "OUTSIDE" {
			close(done)
			wg.Wait()
			t.Fatalf("root swap redirected undo: %q", got)
		}
		reset()
	}
	close(done)
	wg.Wait()
}

// TestUndoMoveCrossRootKeepsSource (REVIEW-F2-SEC5-17): a move whose paths
// are in different sandbox roots is refused before the source is removed.
func TestUndoMoveCrossRootKeepsSource(t *testing.T) {
	base := t.TempDir()
	r1, r2 := filepath.Join(base, "r1"), filepath.Join(base, "r2")
	for _, d := range []string{r1, r2} {
		if err := os.Mkdir(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	sb, err := sandbox.New(&sandbox.Config{Roots: []string{r1, r2}, Home: base})
	if err != nil {
		t.Fatal(err)
	}
	f := newFake()
	u, err := NewUndoer(f, sb)
	if err != nil {
		t.Fatal(err)
	}
	src, dst := filepath.Join(r1, "a"), filepath.Join(r2, "b")
	write(t, src, []byte("body"), 0o600)
	write(t, dst, []byte("body"), 0o600)
	a := action(1, FileMove, src, f.put([]byte("body"), 0o600), f.put([]byte("body"), 0o600))
	a.MoveTo = dst
	f.add(a)
	if _, err := u.Undo(t.Context(), "s1", 1, true); err == nil {
		t.Fatal("cross-root move undo succeeded")
	}
	if !present(src) {
		t.Fatal("cross-root move removed the source before refusing")
	}
}
