package code

import (
	"context"
	"errors"
	"fmt"
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
	r, err := New(Config{Backend: b, Env: env, Lookup: stubLookup})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

// stubLookup is the test seam: the exec contract is proven without any
// interpreter installed, on every OS.
func stubLookup(Language, []string) ([]string, error) { return []string{"/bin/true"}, nil }

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
	if spec.Argv[0] != "/bin/true" || !strings.HasSuffix(spec.Argv[len(spec.Argv)-1], "main.py") {
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

func TestRunMissingInterpreterNamesCandidatesAndCleans(t *testing.T) {
	t.Parallel()
	b := newFake()
	root := t.TempDir()
	r, err := New(Config{Backend: b, Root: root, Lookup: func(Language, []string) ([]string, error) {
		return nil, fmt.Errorf("%w: interpreter for python not found (tried python3)", exec.ErrUnavailable)
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(t.Context(), &Request{Language: Python, Code: "1"})
	if !errors.Is(err, exec.ErrUnavailable) || !strings.Contains(err.Error(), "python3") {
		t.Fatalf("err = %v", err)
	}
	entries, rerr := os.ReadDir(root)
	if rerr != nil || len(entries) != 0 {
		t.Fatalf("temp dir not cleaned: %v %v", entries, rerr)
	}
	if b.count() != 0 {
		t.Fatalf("spawned %d", b.count())
	}
}

func TestCandidatesPerOS(t *testing.T) {
	t.Parallel()
	cases := []struct {
		lang Language
		goos string
		want string
	}{
		{Python, "linux", "python3"},
		{Python, "windows", "python"},
		{JavaScript, "windows", "node"},
		{Go, "windows", "go"},
	}
	for _, tc := range cases {
		names, _, file, err := candidates(tc.lang, tc.goos)
		if err != nil || names[0] != tc.want || file == "" {
			t.Fatalf("%s/%s = %v %q %v", tc.lang, tc.goos, names, file, err)
		}
	}
	if _, _, _, err := candidates("rust", "linux"); !errors.Is(err, ErrLanguage) {
		t.Fatalf("rust = %v", err)
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
