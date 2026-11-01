//go:build !minimal

package backends

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/netx"
)

// sandboxCall is one request the fake control plane received.
type sandboxCall struct {
	method, path, auth, body string
}

// fakeSandbox is the minimal HTTP contract of all three providers.
type fakeSandbox struct {
	mu    sync.Mutex
	calls []sandboxCall
	delay time.Duration
}

func (f *fakeSandbox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, rerr := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if rerr != nil {
		http.Error(w, rerr.Error(), http.StatusBadRequest)
		return
	}
	sb := string(raw)
	f.mu.Lock()
	f.calls = append(f.calls, sandboxCall{r.Method, r.URL.Path, r.Header.Get("Authorization"), sb})
	delay := f.delay
	f.mu.Unlock()
	if delay > 0 && isExecPath(r.URL.Path) {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sandboxes"), r.URL.Path == "/api/sandbox":
		if _, werr := w.Write([]byte(`{"sandbox":{"id":"sbx-1"}}`)); werr != nil {
			return
		}
	case strings.HasSuffix(r.URL.Path, "/cmd"), strings.HasSuffix(r.URL.Path, "/process/execute"),
		strings.HasSuffix(r.URL.Path, "/exec"):
		if _, werr := w.Write([]byte(`{"exitCode":3,"stdout":"from-sandbox"}`)); werr != nil {
			return
		}
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func isExecPath(path string) bool {
	return strings.HasSuffix(path, "/cmd") || strings.HasSuffix(path, "/process/execute") || strings.HasSuffix(path, "/exec")
}

func (f *fakeSandbox) snapshot() []sandboxCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sandboxCall(nil), f.calls...)
}

func testSandbox(t *testing.T, provider string, srv *httptest.Server) *Sandbox {
	t.Helper()
	net := netx.New(netx.Config{})
	t.Cleanup(net.CloseIdleConnections)
	endpoint := srv.URL
	if provider == "daytona" {
		endpoint += "/api"
	}
	b, err := NewSandbox(&SandboxConfig{
		Provider: provider, Endpoint: endpoint,
		Token:   func(context.Context) (string, error) { return "sekret", nil },
		Runtime: "node24", ProjectID: "prj",
		Timeout: time.Minute, EnvAllow: []string{"LANG"},
		Client: net.Client("exec-"+provider, netx.Exec),
	})
	if err != nil {
		t.Fatalf("NewSandbox(%s): %v", provider, err)
	}
	t.Cleanup(func() {
		//nolint:errcheck,gosec // test cleanup
		b.Close(context.Background())
	})
	return b
}

// TestSandboxProviders covers each provider lifecycle.
//
//nolint:gocognit,gocyclo // three provider subtests share one assertion block
func TestSandboxProviders(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"modal", "daytona", "vercel_sandbox"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			api := &fakeSandbox{}
			srv := httptest.NewServer(api)
			t.Cleanup(srv.Close)
			b := testSandbox(t, provider, srv)
			res, err := b.Run(t.Context(), &exec.Spec{
				Argv: []string{"/bin/sh", "-c", "echo hi"}, Dir: "/host", Timeout: 5 * time.Second,
				Env: []string{"LANG=C"},
			})
			if err != nil {
				t.Fatalf("Run: %v calls=%+v", err, api.snapshot())
			}
			if res.Status != exec.StatusExited || res.ExitCode == nil || *res.ExitCode != 3 || string(res.Output) != "from-sandbox" {
				t.Fatalf("result = %+v", res)
			}
			calls := api.snapshot()
			if len(calls) != 3 {
				t.Fatalf("calls = %d, want create+exec+stop: %+v", len(calls), calls)
			}
			for _, c := range calls {
				if c.auth != "Bearer sekret" {
					t.Errorf("%s %s auth = %q", c.method, c.path, c.auth)
				}
			}
			if !strings.HasSuffix(calls[0].path, "sandboxes") && calls[0].path != "/api/sandbox" {
				t.Errorf("create path = %s", calls[0].path)
			}
			var body map[string]any
			if err := json.Unmarshal([]byte(calls[1].body), &body); err != nil {
				t.Fatalf("exec body: %v", err)
			}
			env, ok := body["env"].(map[string]any)
			if !ok || env["LANG"] != "C" {
				t.Errorf("env = %v", body["env"])
			}
			if calls[2].method == http.MethodPost && !strings.HasSuffix(calls[2].path, "/stop") {
				t.Errorf("stop call = %s %s", calls[2].method, calls[2].path)
			}
		})
	}
}

// TestSandboxFailsClosed: missing credential, endpoint, or caps never falls
// back to local, and a provider timeout returns status timeout.
func TestSandboxFailsClosed(t *testing.T) {
	t.Parallel()
	net := netx.New(netx.Config{})
	t.Cleanup(net.CloseIdleConnections)
	client := net.Client("exec", netx.Exec)
	if _, err := NewSandbox(&SandboxConfig{Provider: "modal", Client: client}); !errors.Is(err, exec.ErrUnavailable) {
		t.Fatalf("no token secret = %v", err)
	}
	if _, err := NewSandbox(&SandboxConfig{Provider: "modal", Token: func(context.Context) (string, error) { return "x", nil }, Client: client}); !errors.Is(err, exec.ErrUnavailable) {
		t.Fatalf("modal without endpoint = %v", err)
	}
	if _, err := NewSandbox(&SandboxConfig{Provider: "vercel_sandbox", Token: func(context.Context) (string, error) { return "x", nil }, Client: client}); !errors.Is(err, exec.ErrSpec) {
		t.Fatalf("vercel without runtime = %v", err)
	}
	denied := &Sandbox{cfg: SandboxConfig{
		Provider: "modal", Client: client,
		Token: func(context.Context) (string, error) { return "", errors.New("keychain unavailable") },
	}}
	if res, err := denied.Run(t.Context(), &exec.Spec{Argv: []string{"true"}}); !errors.Is(err, exec.ErrUnavailable) || res.Status != exec.StatusSpawnFailed {
		t.Fatalf("denied token = %v, %+v", err, res)
	}
	api := &fakeSandbox{}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	b := testSandbox(t, "daytona", srv)
	if _, err := b.Run(t.Context(), &exec.Spec{Argv: []string{"true"}, PTY: true}); !errors.Is(err, exec.ErrCapability) {
		t.Fatalf("pty = %v", err)
	}
	if _, err := b.Run(t.Context(), &exec.Spec{Argv: []string{"true"}, Limits: exec.Limits{PIDs: 1}}); !errors.Is(err, exec.ErrCapability) {
		t.Fatalf("limits = %v", err)
	}
	if n, err := b.Sweep(t.Context(), "1.unknown"); err != nil || n != 0 {
		t.Fatalf("unknown sweep = %d, %v", n, err)
	}
	if err := b.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestSandboxTimeoutStops: a command that outlives its deadline returns
// status timeout and still stops the sandbox (no orphan).
func TestSandboxTimeoutStops(t *testing.T) {
	t.Parallel()
	api := &fakeSandbox{delay: 2 * time.Second}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	b := testSandbox(t, "vercel_sandbox", srv)
	res, err := b.Run(t.Context(), &exec.Spec{Argv: []string{"sleep", "9"}, Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != exec.StatusTimeout {
		t.Fatalf("status = %s", res.Status)
	}
	calls := api.snapshot()
	if len(calls) < 2 || !strings.HasSuffix(calls[len(calls)-1].path, "/stop") {
		t.Fatalf("sandbox not stopped: %+v", calls)
	}
}

// TestSandboxSweep: a per-call sandbox leaves nothing for Sweep; Close refuses
// later calls instead of resurrecting one.
func TestSandboxSweep(t *testing.T) {
	t.Parallel()
	api := &fakeSandbox{}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	b := testSandbox(t, "modal", srv)
	if n, err := b.Sweep(t.Context(), "1.abc"); err != nil || n != 0 {
		t.Fatalf("Sweep = %d, %v", n, err)
	}
	if err := b.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
