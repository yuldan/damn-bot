//go:build unix

package sandbox

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
	"golang.org/x/sys/unix"
)

// TestOpenResolvedSwapRace (CT27-1, CT27-6): while OpenResolved runs, a
// flipper keeps swapping an ancestor directory of the opened file for a
// symlink to the deny dir ~/.ssh, which holds a file of the same name. In
// both open modes and both branches (inside a root, unapproved; outside
// every root, approved) no open may return the deny file's content, and
// the race must have produced both opens and refusals.
func TestOpenResolvedSwapRace(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mkfile(t, filepath.Join(f.home, ".ssh", "f.txt"), "key")
	modes(t, f.sb, func(t *testing.T, sb *Sandbox) {
		for base, approved := range map[string]bool{f.root: false, f.out: true} {
			swapRace(t, sb, filepath.Join(base, filepath.Base(t.Name())), filepath.Join(f.home, ".ssh"), approved)
		}
	})
}

// swapRace runs the race of TestOpenResolvedSwapRace below dir.
func swapRace(t *testing.T, sb *Sandbox, dir, deny string, approved bool) {
	t.Helper()
	d, lnk, park := filepath.Join(dir, "d"), filepath.Join(dir, "lnk"), filepath.Join(dir, "park")
	mkfile(t, filepath.Join(d, "f.txt"), "data")
	symlink(t, deny, lnk)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			// d is the real directory, then the symlink to deny, then real.
			for _, mv := range [][2]string{{d, park}, {lnk, d}, {d, lnk}, {park, d}} {
				if err := os.Rename(mv[0], mv[1]); err != nil {
					t.Errorf("Rename: %v", err)
					return
				}
			}
		}
	})
	p := filepath.Join(d, "f.txt")
	opened, refused := 0, 0
	deadline := time.Now().Add(10 * time.Second)
	for i := 0; i < 2000 || (opened == 0 || refused == 0) && time.Now().Before(deadline); i++ {
		file, _, err := sb.OpenResolved(t.Context(), p, approved)
		if err != nil || file == nil {
			refused++
			continue
		}
		opened++
		b, rerr := io.ReadAll(file)
		closeT(t, file)
		if rerr != nil || string(b) != "data" {
			t.Errorf("OpenResolved(%s, approved %v) read %q, %v: deny content leaked", p, approved, b, rerr)
			break
		}
	}
	close(stop)
	wg.Wait()
	t.Logf("approved %v: opened %d, refused %d", approved, opened, refused)
	if opened == 0 || refused == 0 {
		t.Errorf("approved %v: opened %d, refused %d; want both > 0", approved, opened, refused)
	}
}

// TestOpenNonRegular (CT27-4): a FIFO inside the root or a device reached
// by approval is refused with ErrNotRegular by every open path, read and
// write, in both open modes, without ever blocking (a reader-less FIFO
// blocks a plain open forever).
func TestOpenNonRegular(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	f := newFixture(t)
	fifo := filepath.Join(f.root, "pipe")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}
	if _, _, err := f.sb.Check(t.Context(), fifo, OpRead); !errors.Is(err, ErrNotRegular) {
		t.Errorf("Check(fifo) = %v, want ErrNotRegular", err)
	}
	fallback := *f.sb
	fallback.noOpenat2 = true
	for _, sb := range []*Sandbox{f.sb, &fallback} {
		ctx := t.Context()
		for name, open := range map[string]func() (*os.File, error){
			"OpenFile(fifo, read)":      func() (*os.File, error) { return first(sb.OpenFile(ctx, fifo, os.O_RDONLY, 0)) },
			"OpenFile(fifo, write)":     func() (*os.File, error) { return first(sb.OpenFile(ctx, fifo, os.O_WRONLY, 0)) },
			"OpenApproved(fifo)":        func() (*os.File, error) { return sb.OpenApproved(ctx, fifo, os.O_RDONLY, 0) },
			"OpenResolved(fifo)":        func() (*os.File, error) { return first(sb.OpenResolved(ctx, fifo, false)) },
			"openVerified(fifo, read)":  func() (*os.File, error) { return first(sb.openVerified(f.root, fifo, os.O_RDONLY, 0, true)) },
			"openVerified(fifo, write)": func() (*os.File, error) { return first(sb.openVerified(f.root, fifo, os.O_WRONLY, 0, true)) },
			"OpenApproved(/dev/null)":   func() (*os.File, error) { return sb.OpenApproved(ctx, "/dev/null", os.O_WRONLY, 0) },
			"OpenResolved(/dev/null)":   func() (*os.File, error) { return first(sb.OpenResolved(ctx, "/dev/null", true)) },
		} {
			refusedNonBlocking(t, name, fifo, open)
		}
	}
}

func first(f *os.File, _ Verdict, err error) (*os.File, error) { return f, err }

// refusedNonBlocking runs open with a 1 s deadline: it must fail with
// ErrNotRegular. An open still blocked at the deadline is released by
// opening fifo read-write, so the failure is reported instead of hanging.
func refusedNonBlocking(t *testing.T, name, fifo string, open func() (*os.File, error)) {
	t.Helper()
	type result struct {
		f   *os.File
		err error
	}
	done := make(chan result, 1)
	go func() {
		f, err := open()
		done <- result{f, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-time.After(time.Second):
		t.Errorf("%s blocked for more than 1 s", name)
		if u, err := os.OpenFile(fifo, os.O_RDWR, 0); err == nil { //nolint:gosec // G304: test fixture FIFO
			defer closeT(t, u)
		}
		r = <-done
	}
	if r.f != nil {
		closeT(t, r.f)
	}
	if r.f != nil || !errors.Is(r.err, ErrNotRegular) {
		t.Errorf("%s = %v, %v; want ErrNotRegular", name, r.f, r.err)
	}
}
