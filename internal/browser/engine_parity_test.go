//go:build !minimal

package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// TestF6EXEC037BrowserLocalEngine (EXEC-037): the local Chromium engine is a
// CDP target that fails closed. A dial failure is a typed error, not a silent
// success, and no other configured endpoint is tried as an implicit fallback.
func TestF6EXEC037BrowserLocalEngine(t *testing.T) {
	t.Parallel()

	t.Run("dial failure is typed and does not fall back", func(t *testing.T) {
		t.Parallel()
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusTeapot)
		}))
		defer srv.Close()
		cfg := ModuleConfig{
			Enabled: true, Engine: "cdp",
			CDPURL:       "ws://127.0.0.1:9222/devtools/page/x",
			CamofoxURL:   srv.URL,
			AllowOrigins: []string{"https://app.example"},
		}
		m := newModule(t.Context(), t, &cfg, Deps{
			Dial: func(context.Context, string, time.Duration) (Transport, error) {
				return nil, errors.New("dial refused")
			},
			HTTP: srv.Client(),
		})
		for i := range 2 {
			_, err := m.Invoke(t.Context(), "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
			if got := toolErrorCode(t, err); got != "network" {
				t.Fatalf("call %d: code = %s, want network", i, got)
			}
			if !strings.Contains(err.Error(), "dial refused") {
				t.Fatalf("call %d: err = %v", i, err)
			}
		}
		if hits.Load() != 0 {
			t.Fatalf("fallback endpoint was called %d times", hits.Load())
		}
	})

	t.Run("missing endpoint fails closed", func(t *testing.T) {
		t.Parallel()
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()
		cfg := ModuleConfig{
			Enabled: true, Engine: "cdp", CamofoxURL: srv.URL,
			AllowOrigins: []string{"https://app.example"},
		}
		m := newModule(t.Context(), t, &cfg, Deps{HTTP: srv.Client()})
		_, err := m.Invoke(t.Context(), "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
		if got := toolErrorCode(t, err); got != "module_unavailable" {
			t.Fatalf("code = %s, want module_unavailable", got)
		}
		if hits.Load() != 0 {
			t.Fatalf("fallback endpoint was called %d times", hits.Load())
		}
	})
}

// TestF6EXEC038Camofox (EXEC-038): the Camoufox sidecar keeps the same page
// policy as CDP, including a redirect to a private target and typed refusals.
func TestF6EXEC038Camofox(t *testing.T) {
	t.Parallel()

	t.Run("navigate and act cross the sidecar wire", func(t *testing.T) {
		t.Parallel()
		rec := &recorded{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec.add(r)
			if r.URL.Path == "/v1/navigate" {
				writeBody(t, w, `{"url":"https://app.example/dashboard"}`)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()
		appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
		cfg := camofoxConfig(srv.URL)
		m := newModule(t.Context(), t, &cfg, Deps{HTTP: srv.Client(), Approvals: appr})
		res, err := m.Invoke(t.Context(), "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
		if err != nil || res.UntrustedSource != "browser:app.example" {
			t.Fatalf("navigate = %+v, %v", res, err)
		}
		if _, _, body := rec.last(); !strings.Contains(body, `"url":"https://app.example/"`) {
			t.Fatalf("navigate body = %s", body)
		}
		if _, err := m.Invoke(t.Context(), "browser_click", json.RawMessage(`{"x":4,"y":5}`), host.Env{CallID: "c1"}); err != nil {
			t.Fatalf("click: %v", err)
		}
		if _, path, body := rec.last(); path != "/v1/act" || !strings.Contains(body, `"kind":"click"`) {
			t.Fatalf("act = %s %s", path, body)
		}
	})

	t.Run("redirect to a private target is blocked", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeBody(t, w, `{"url":"http://169.254.169.254/latest/meta-data/"}`)
		}))
		defer srv.Close()
		cfg := camofoxConfig(srv.URL)
		m := newModule(t.Context(), t, &cfg, Deps{HTTP: srv.Client()})
		_, err := m.Invoke(t.Context(), "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
		if got := toolErrorCode(t, err); got != "blocked_host" {
			t.Fatalf("code = %s, want blocked_host", got)
		}
	})

	t.Run("sidecar refusal is typed", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		cfg := camofoxConfig(srv.URL)
		m := newModule(t.Context(), t, &cfg, Deps{HTTP: srv.Client()})
		_, err := m.Invoke(t.Context(), "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
		if got := toolErrorCode(t, err); got != "network" {
			t.Fatalf("code = %s, want network", got)
		}
	})
}

// TestF6EXEC039LightpandaFallback (EXEC-039): Lightpanda is fetch-only. Every
// interactive operation is module_unavailable, and neither a failed fetch nor
// an unsupported operation silently falls back to another engine.
func TestF6EXEC039LightpandaFallback(t *testing.T) {
	t.Parallel()
	t.Run("navigation uses the CLI and interactive operations fail closed", func(t *testing.T) {
		t.Parallel()
		lightpandaNavigation(t)
	})
	t.Run("fetch failure is typed and does not fall back", func(t *testing.T) {
		t.Parallel()
		lightpandaFailure(t)
	})
}

// lightpandaModule also exposes a Camofox endpoint so a silent fallback would
// be observable; the explicit engine must never reach it.
func lightpandaModule(t *testing.T, run RunFunc) (*Module, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	cfg := ModuleConfig{
		Enabled: true, Engine: "lightpanda",
		LightpandaBin: filepath.Join(t.TempDir(), "lightpanda"),
		CamofoxURL:    srv.URL, AllowOrigins: []string{"https://app.example"},
	}
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	return newModule(t.Context(), t, &cfg, Deps{Run: run, Approvals: appr, HTTP: srv.Client()}), &hits
}

func assertUnavailable(t *testing.T, m *Module, tools ...struct{ tool, args string }) {
	t.Helper()
	for _, tc := range tools {
		_, err := m.Invoke(t.Context(), tc.tool, json.RawMessage(tc.args), host.Env{CallID: "l1"})
		if got := toolErrorCode(t, err); got != "module_unavailable" {
			t.Fatalf("%s: code = %s, want module_unavailable", tc.tool, got)
		}
	}
}

func lightpandaNavigation(t *testing.T) {
	t.Helper()
	var runs atomic.Int32
	var argv []string
	m, camofoxHits := lightpandaModule(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		runs.Add(1)
		argv = append([]string{name}, args...)
		return []byte("<html>ok</html>"), nil
	})
	res, err := m.Invoke(t.Context(), "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
	if err != nil || res.UntrustedSource != "browser:app.example" {
		t.Fatalf("navigate = %+v, %v", res, err)
	}
	if !strings.HasSuffix(argv[0], string(filepath.Separator)+"lightpanda") ||
		strings.Join(argv[1:], " ") != "fetch --dump https://app.example/" {
		t.Fatalf("argv = %v", argv)
	}
	assertUnavailable(t, m,
		struct{ tool, args string }{"browser_eval", `{"expression":"1"}`},
		struct{ tool, args string }{"browser_screenshot", `{}`},
		struct{ tool, args string }{"browser_snapshot", `{}`},
		struct{ tool, args string }{"browser_click", `{"x":1,"y":2}`},
		struct{ tool, args string }{"browser_type", `{"text":"x"}`},
		struct{ tool, args string }{"browser_press", `{"key":"Enter"}`},
		struct{ tool, args string }{"browser_scroll", `{"delta_x":1,"delta_y":1}`},
	)
	if runs.Load() != 1 {
		t.Fatalf("CLI runs = %d, want only the navigation", runs.Load())
	}
	if camofoxHits.Load() != 0 {
		t.Fatalf("camofox fallback was called %d times", camofoxHits.Load())
	}
}

func lightpandaFailure(t *testing.T) {
	t.Helper()
	m, camofoxHits := lightpandaModule(t, func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("exit 1")
	})
	_, err := m.Invoke(t.Context(), "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
	if got := toolErrorCode(t, err); got != "network" {
		t.Fatalf("code = %s, want network", got)
	}
	if camofoxHits.Load() != 0 {
		t.Fatalf("camofox fallback was called %d times", camofoxHits.Load())
	}
}
