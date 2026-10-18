package boundary

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

var errBoom = errors.New("boom")

// panicSecret is a panic value standing in for message content; it must
// reach the owner in the error but never the log (P29).
const panicSecret = "user said hunter2"

// explode is the only provoked panic of these tests: recovering it is what
// is under test (E-24).
func explode(v any) error {
	panic(v) //nolint:forbidigo // provokes the panic boundary must recover.
}

func TestRunReturnsError(t *testing.T) {
	t.Parallel()
	if err := Run(t.Context(), "ok", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("Run ok = %v", err)
	}
	err := Run(t.Context(), "fail", func(context.Context) error { return errBoom })
	if !errors.Is(err, errBoom) || errors.Is(err, ErrPanic) {
		t.Fatalf("Run fail = %v", err)
	}
}

// TestRunPanic (E-24, P14): panics of every kind become a *PanicError that
// carries name, value and stack; runtime errors stay matchable.
func TestRunPanic(t *testing.T) {
	t.Parallel()
	var nilMap map[string]int
	for _, tc := range []struct {
		name string
		fn   func(context.Context) error
		is   func(error) bool
	}{
		{"value", func(context.Context) error { return explode(panicSecret) }, func(error) bool { return true }},
		{"error", func(context.Context) error { return explode(errBoom) }, func(err error) bool { return errors.Is(err, errBoom) }},
		{"runtime", func(context.Context) error { nilMap["x"] = 1; return nil }, func(err error) bool {
			var re runtime.Error
			return errors.As(err, &re)
		}},
		{"nil", func(context.Context) error { return explode(nil) }, func(err error) bool {
			var pn *runtime.PanicNilError
			return errors.As(err, &pn)
		}},
	} {
		err := Run(t.Context(), tc.name, tc.fn)
		var pe *PanicError
		if !errors.Is(err, ErrPanic) || !errors.As(err, &pe) || !tc.is(err) {
			t.Fatalf("%s: Run = %v", tc.name, err)
		}
		if pe.Name != tc.name || !bytes.Contains(pe.Stack, []byte("boundary_test.go")) ||
			!strings.Contains(err.Error(), "panic in "+tc.name) {
			t.Fatalf("%s: PanicError = %+v", tc.name, pe)
		}
	}
}

// TestPanicLogged: the recovery is logged at ERROR with name, type and
// stack, without the panic value. Not parallel: it swaps slog's default.
func TestPanicLogged(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	if err := Run(t.Context(), "logged", func(context.Context) error { return explode(panicSecret) }); !errors.Is(err, ErrPanic) {
		t.Fatalf("Run = %v", err)
	}
	out := buf.String()
	for _, want := range []string{"level=ERROR", `msg="panic recovered"`, "goroutine=logged", "panic_type=string", "stack="} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q lacks %q", out, want)
		}
	}
	if strings.Contains(out, "hunter2") {
		t.Errorf("log leaks the panic value: %q", out)
	}
}

// TestGo (E-26, E-29): the goroutine reports its error or panic to Wait, and
// returns once its context is canceled.
func TestGo(t *testing.T) {
	t.Parallel()
	if err := Go(t.Context(), "fail", func(context.Context) error { return errBoom }).Wait(); !errors.Is(err, errBoom) {
		t.Fatalf("Wait = %v", err)
	}
	if err := Go(t.Context(), "panic", func(context.Context) error { return explode(errBoom) }).Wait(); !errors.Is(err, ErrPanic) {
		t.Fatalf("Wait = %v", err)
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		task := Go(ctx, "worker", func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
		synctest.Wait()
		select {
		case <-task.Done():
			t.Fatal("task returned before cancel")
		default:
		}
		cancel()
		if err := task.Wait(); !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait = %v", err)
		}
	})
}

// TestGroupLimit (E-27): SetLimit bounds the active members.
func TestGroupLimit(t *testing.T) {
	t.Parallel()
	const limit, members = 2, 6
	synctest.Test(t, func(t *testing.T) {
		g, ctx := WithContext(t.Context())
		g.SetLimit(limit)
		var active, peak atomic.Int32
		release := make(chan struct{}) //damnbot:unbuffered close-only release signal
		launcher := Go(ctx, "launcher", func(ctx context.Context) error {
			for range members {
				g.Go(ctx, "member", func(context.Context) error {
					n := active.Add(1)
					peak.Store(max(peak.Load(), n))
					<-release
					active.Add(-1)
					return nil
				})
			}
			return nil
		})
		synctest.Wait()
		if n := active.Load(); n != limit {
			t.Fatalf("active = %d, want %d", n, limit)
		}
		close(release)
		if err := launcher.Wait(); err != nil {
			t.Fatalf("launcher: %v", err)
		}
		if err := g.Wait(); err != nil {
			t.Fatalf("Wait = %v", err)
		}
		if p := peak.Load(); p > limit {
			t.Fatalf("peak = %d > %d", p, limit)
		}
	})
}

// TestGroupPanicCancels: a member panic is the group error and cancels the
// group context, so the other members return (E-29).
func TestGroupPanicCancels(t *testing.T) {
	t.Parallel()
	g, ctx := WithContext(t.Context())
	g.SetLimit(2)
	g.Go(ctx, "waiter", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	g.Go(ctx, "crash", func(context.Context) error { return explode(errBoom) })
	err := g.Wait()
	var pe *PanicError
	if !errors.As(err, &pe) || pe.Name != "crash" || !errors.Is(err, errBoom) {
		t.Fatalf("Wait = %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("group context not canceled")
	}
	if err := g.Wait(); !errors.Is(err, ErrPanic) {
		t.Fatalf("second Wait = %v", err)
	}
}
