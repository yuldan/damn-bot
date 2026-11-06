package gwpeer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
)

func TestF7GW016ProfileRouting(t *testing.T) {
	t.Parallel()
	var seenA, seenB atomic.Int32
	mux := NewMux()
	if err := mux.Add(Profile{
		Name: "profile-a", Platform: "telegram", Account: "bot-a",
		Handler: func(_ context.Context, in *gateway.Inbound) error {
			if in.ChatID != "chat-1" {
				return fmt.Errorf("chat = %q", in.ChatID)
			}
			seenA.Add(1)
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := mux.Add(Profile{
		Name: "profile-b", Platform: "telegram", Account: "bot-b",
		Handler: func(context.Context, *gateway.Inbound) error {
			seenB.Add(1)
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := mux.Add(Profile{Name: "dup", Platform: "telegram", Account: "bot-a", Handler: func(context.Context, *gateway.Inbound) error { return nil }}); !errors.Is(err, ErrProfileDup) {
		t.Fatalf("duplicate route = %v, want ErrProfileDup", err)
	}
	in := &gateway.Inbound{Platform: "telegram", Account: "bot-a", ChatID: "chat-1"}
	for range 3 {
		if err := mux.Dispatch(t.Context(), in); err != nil {
			t.Fatal(err)
		}
	}
	if seenA.Load() != 3 || seenB.Load() != 0 {
		t.Fatalf("routing counts a=%d b=%d", seenA.Load(), seenB.Load())
	}
	in.Account = "bot-b"
	if err := mux.Dispatch(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if seenB.Load() != 1 {
		t.Fatalf("profile-b count = %d, want 1", seenB.Load())
	}
	in.Account = "unknown"
	if err := mux.Dispatch(t.Context(), in); !errors.Is(err, ErrNoProfile) {
		t.Fatalf("unknown route = %v, want ErrNoProfile", err)
	}
	if got := mux.Profiles(); len(got) != 2 || got[0] != "profile-a" || got[1] != "profile-b" {
		t.Fatalf("profiles = %v", got)
	}
}

type fakeConn struct{}

func (fakeConn) Close() error { return nil }

func TestF7ECSTATE004ReadPool(t *testing.T) {
	t.Parallel()
	checkReadPool(t)
}

func checkReadPool(t *testing.T) {
	t.Helper()
	var fallbacks atomic.Int32
	var open atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	var wg sync.WaitGroup
	pool := &ReadPool{
		Max: 2,
		Open: func(context.Context) (io.Closer, error) {
			open.Add(1)
			return fakeConn{}, nil
		},
		Writer: func(context.Context) error {
			fallbacks.Add(1)
			return nil
		},
	}
	for range 2 {
		wg.Go(func() {
			_, err := pool.With(t.Context(), func(context.Context, io.Closer) error {
				started <- struct{}{}
				<-release
				return nil
			})
			if err != nil {
				t.Errorf("With: %v", err)
			}
		})
	}
	<-started
	<-started
	fallback, err := pool.With(t.Context(), func(context.Context, io.Closer) error { return nil })
	if err != nil || !fallback || fallbacks.Load() != 1 {
		t.Fatalf("cap fallback = %v, %v, count=%d", fallback, err, fallbacks.Load())
	}
	close(release)
	wg.Wait()
	if open.Load() != 2 {
		t.Fatalf("opened = %d, want 2 (global cap)", open.Load())
	}

	emfile := &ReadPool{
		Max: 4,
		Open: func(context.Context) (io.Closer, error) {
			return nil, fmt.Errorf("open: %w", syscall.EMFILE)
		},
		Writer: func(context.Context) error {
			fallbacks.Add(1)
			return nil
		},
	}
	fallback, err = emfile.With(t.Context(), func(context.Context, io.Closer) error { return nil })
	if err != nil || !fallback {
		t.Fatalf("EMFILE fallback = %v, %v", fallback, err)
	}
	if fallbacks.Load() != 2 {
		t.Fatalf("writer fallbacks = %d, want 2", fallbacks.Load())
	}
}

func TestF7ECTERM004SandboxKey(t *testing.T) {
	t.Parallel()
	checkSandboxKey(t)
}

func checkSandboxKey(t *testing.T) {
	t.Helper()
	k1, err := SandboxKey("profile-a", "session-1", "task-9")
	if err != nil {
		t.Fatal(err)
	}
	k1b, err := SandboxKey("profile-a", "session-1", "task-9")
	if err != nil || k1 != k1b {
		t.Fatalf("stable key = %q/%q, %v", k1, k1b, err)
	}
	k2, err := SandboxKey("profile-b", "session-1", "task-9")
	if err != nil {
		t.Fatal(err)
	}
	if k1 == k2 {
		t.Fatal("same task id across profiles shares one key")
	}
	d1, err := SandboxDir("/work", "profile-a", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	d2, err := SandboxDir("/work", "profile-b", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/work", "profile-a", "session-1"); d1 == d2 || d1 != want {
		t.Fatalf("sandbox dirs = %q %q, want %q", d1, d2, want)
	}
	if _, err := SandboxKey("..", "session", "task"); !errors.Is(err, ErrInvalidSandbox) {
		t.Fatalf("traversal key = %v, want ErrInvalidSandbox", err)
	}
}

type policyFunc func(ctx context.Context, profile string) (TerminalPolicy, bool, error)

func (f policyFunc) ResolveTerminalPolicy(ctx context.Context, profile string) (TerminalPolicy, bool, error) {
	return f(ctx, profile)
}

func TestF7ECTERM005PolicyResolve(t *testing.T) {
	t.Parallel()
	checkPolicyResolve(t)
}

func checkPolicyResolve(t *testing.T) {
	t.Helper()
	if _, err := ResolveTerminalPolicy(t.Context(), nil, "p"); !errors.Is(err, ErrPolicyUnresolved) {
		t.Fatalf("nil resolver = %v", err)
	}
	if _, err := ResolveTerminalPolicy(t.Context(), policyFunc(func(context.Context, string) (TerminalPolicy, bool, error) {
		return TerminalPolicy{}, false, nil
	}), "p"); !errors.Is(err, ErrPolicyUnresolved) {
		t.Fatalf("missing policy = %v", err)
	}
	if _, err := ResolveTerminalPolicy(t.Context(), policyFunc(func(context.Context, string) (TerminalPolicy, bool, error) {
		return TerminalPolicy{}, false, errors.New("boom")
	}), "p"); !errors.Is(err, ErrPolicyUnresolved) {
		t.Fatalf("resolver error = %v", err)
	}
	if _, err := ResolveTerminalPolicy(t.Context(), policyFunc(func(context.Context, string) (TerminalPolicy, bool, error) {
		return TerminalPolicy{Profile: "other", Shell: "/bin/sh"}, true, nil
	}), "p"); !errors.Is(err, ErrPolicyUnresolved) {
		t.Fatalf("mismatched profile = %v", err)
	}
	got, err := ResolveTerminalPolicy(t.Context(), policyFunc(func(context.Context, string) (TerminalPolicy, bool, error) {
		return TerminalPolicy{Profile: "p", Shell: "/bin/sh", AllowDestructive: true}, true, nil
	}), "p")
	if err != nil || got.Shell != "/bin/sh" || !got.AllowDestructive {
		t.Fatalf("resolved policy = %+v, %v", got, err)
	}
}
