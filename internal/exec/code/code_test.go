package code

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

// mustRunner builds a Runner for a test backend.
func mustRunner(t *testing.T, b exec.Backend, env []string) *Runner {
	t.Helper()
	r, err := New(Config{Backend: b, Env: env})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

// fakeBackend records the Spec and returns a canned Result.
type fakeBackend struct {
	mu    sync.Mutex
	specs []exec.Spec
	res   exec.Result
	err   error
	caps  exec.Caps
}

func (f *fakeBackend) ID() string      { return "fake" }
func (f *fakeBackend) Caps() exec.Caps { return f.caps }
func (f *fakeBackend) Run(_ context.Context, s *exec.Spec) (exec.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs = append(f.specs, *s)
	return f.res, f.err
}
func (f *fakeBackend) Sweep(context.Context, string) (int, error) { return 0, nil }
func (f *fakeBackend) Close(context.Context) error                { return nil }

func (f *fakeBackend) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.specs) }

func newFake() *fakeBackend {
	return &fakeBackend{
		caps: exec.Caps{Signals: true, Network: true, ResourceLimits: true},
		res:  exec.Result{Status: exec.StatusExited, Output: []byte("ok")},
	}
}

func TestRunWritesTempScriptAndCleansUp(t *testing.T) {
	t.Parallel()
	b := newFake()
	r := mustRunner(t, b, []string{"PATH=" + os.Getenv("PATH") + ":/usr/bin:/bin"})
	res, err := r.Run(t.Context(), &Request{Language: Python, Code: "print(1)", Stdin: []byte(`{"x":1}`)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Backend != "fake" || !strings.Contains(string(res.Stdout), "ok") || res.Isolation.Network != "deny" {
		t.Fatalf("result = %+v", res)
	}
	if b.count() != 1 {
		t.Fatalf("runs = %d", b.count())
	}
	spec := b.specs[0]
	if !strings.HasSuffix(spec.Argv[0], "/python3") || !strings.HasSuffix(spec.Argv[len(spec.Argv)-1], "main.py") {
		t.Fatalf("argv = %v", spec.Argv)
	}
	if string(spec.Stdin) != `{"x":1}` || spec.Timeout != DefaultTimeout || spec.Limits.MemoryMiB != MemoryMiB {
		t.Fatalf("spec = %+v", spec)
	}
	if strings.Contains(spec.Env[0], "SECRET") {
		t.Fatalf("env leaked: %v", spec.Env)
	}
	if _, err := os.Stat(spec.Dir); !os.IsNotExist(err) {
		t.Fatalf("temp dir survived: %v", err)
	}
}

func TestRunRefusesNetworkWithoutBackendCapability(t *testing.T) {
	t.Parallel()
	b := newFake()
	b.caps = exec.Caps{Signals: true}
	r := mustRunner(t, b, nil)
	if _, err := r.Run(t.Context(), &Request{Language: JavaScript, Code: "1"}); !errors.Is(err, ErrNetworkDeny) {
		t.Fatalf("err = %v", err)
	}
	if b.count() != 0 {
		t.Fatalf("spawned despite network deny: %d", b.count())
	}
}

func TestRunRefusesUnsupportedLanguageAndSize(t *testing.T) {
	t.Parallel()
	b := newFake()
	r := mustRunner(t, b, nil)
	if _, err := r.Run(t.Context(), &Request{Language: "rust", Code: "fn main(){}"}); !errors.Is(err, ErrLanguage) {
		t.Fatalf("language err = %v", err)
	}
	if _, err := r.Run(t.Context(), &Request{Language: Go, Code: strings.Repeat("x", MaxCodeBytes+1)}); !errors.Is(err, ErrCodeSize) {
		t.Fatalf("size err = %v", err)
	}
	if b.count() != 0 {
		t.Fatalf("spawned on refusal: %d", b.count())
	}
}

func TestRunTimeoutClampAndNoLimitsClaim(t *testing.T) {
	t.Parallel()
	b := newFake()
	b.caps = exec.Caps{Signals: true, Network: true}
	r := mustRunner(t, b, []string{"PATH=" + os.Getenv("PATH")})
	_, err := r.Run(t.Context(), &Request{Language: Go, Code: "package main", Timeout: time.Hour})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	spec := b.specs[0]
	if spec.Timeout != MaxTimeout {
		t.Fatalf("timeout = %s", spec.Timeout)
	}
	if spec.Limits.Set() {
		t.Fatalf("limits claimed without capability: %+v", spec.Limits)
	}
}

func TestRunPropagatesBackendFailure(t *testing.T) {
	t.Parallel()
	b := newFake()
	b.err = errors.New("boom")
	r := mustRunner(t, b, nil)
	if _, err := r.Run(t.Context(), &Request{Language: Python, Code: "1"}); err == nil {
		t.Fatal("want error")
	}
	if _, err := New(Config{}); !errors.Is(err, ErrBackend) {
		t.Fatalf("nil backend = %v", err)
	}
}
