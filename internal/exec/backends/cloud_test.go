package backends

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

// fakeCloud serves the documented adapter endpoint and records requests.
func fakeCloud(t *testing.T) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var posts, deletes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodPost:
			posts.Add(1)
			var req cloudRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			if req.Env["OPENAI_API_KEY"] != "" {
				http.Error(w, "secret leaked", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			//nolint:errcheck,gosec // the response was already committed
			json.NewEncoder(w).Encode(cloudResponse{ID: "sbx-1", ExitCode: 3, Stdout: "out:" + strings.Join(req.Argv, ","), Stderr: ":err"})
		case http.MethodDelete:
			deletes.Add(1)
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &posts, &deletes
}

func TestCloudFailClosedWithoutCredentials(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"modal", "daytona", "vercel_sandbox"} {
		if _, err := NewCloud(&CloudConfig{Provider: p, Endpoint: "https://example.test"}); !errors.Is(err, exec.ErrUnavailable) {
			t.Errorf("%s without token = %v", p, err)
		}
		if _, err := NewCloud(&CloudConfig{Provider: "other", Endpoint: "https://example.test", Token: "t"}); !errors.Is(err, exec.ErrSpec) {
			t.Errorf("unknown provider = %v", err)
		}
	}
}

func TestCloudRunAndCleanup(t *testing.T) {
	t.Parallel()
	srv, posts, deletes := fakeCloud(t)
	b, err := NewCloud(&CloudConfig{
		Provider: "modal", Endpoint: srv.URL, Token: "secret-token", Timeout: 5 * time.Second,
		EnvAllow: []string{"DAMNBOT_TEST"},
	})
	if err != nil {
		t.Fatalf("NewCloud: %v", err)
	}
	if b.Caps() != (exec.Caps{}) {
		t.Fatalf("caps = %+v, want none", b.Caps())
	}
	res, err := b.Run(t.Context(), &exec.Spec{
		Argv: []string{"/bin/echo", "hi"}, Dir: "/work",
		Env: []string{"DAMNBOT_TEST=1"}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != exec.StatusExited || res.ExitCode == nil || *res.ExitCode != 3 {
		t.Fatalf("status = %s exit = %v", res.Status, res.ExitCode)
	}
	if got := string(res.Output); !strings.Contains(got, "out:/bin/echo,hi") || !strings.Contains(got, ":err") {
		t.Fatalf("output = %q", got)
	}
	if _, err := b.Sweep(t.Context(), "sbx-1"); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if err := b.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if posts.Load() != 1 || deletes.Load() < 1 {
		t.Fatalf("posts=%d deletes=%d", posts.Load(), deletes.Load())
	}
}

func TestCloudRejectsSecretsAndCaps(t *testing.T) {
	t.Parallel()
	srv, _, _ := fakeCloud(t)
	b, err := NewCloud(&CloudConfig{Provider: "daytona", Endpoint: srv.URL, Token: "secret-token"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Run(t.Context(), &exec.Spec{
		Argv: []string{"/bin/true"}, Env: []string{"OPENAI_API_KEY=sk-x"},
	}); !errors.Is(err, exec.ErrCapability) {
		t.Fatalf("secret env = %v", err)
	}
	if _, err := b.Run(t.Context(), &exec.Spec{Argv: []string{"/bin/true"}, PTY: true}); !errors.Is(err, exec.ErrCapability) {
		t.Fatalf("pty = %v", err)
	}
	if _, err := b.Run(t.Context(), &exec.Spec{Argv: []string{"/bin/true"}, Limits: exec.Limits{PIDs: 2}}); !errors.Is(err, exec.ErrCapability) {
		t.Fatalf("limits = %v", err)
	}
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCloudTimeout(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			return
		}
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	b, err := NewCloud(&CloudConfig{Provider: "vercel_sandbox", Endpoint: srv.URL, Token: "t", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.Run(t.Context(), &exec.Spec{Argv: []string{"/bin/true"}, Timeout: 100 * time.Millisecond})
	if err == nil || res.Status != exec.StatusTimeout {
		t.Fatalf("status = %s err = %v", res.Status, err)
	}
}
