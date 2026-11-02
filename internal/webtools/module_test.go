//go:build !minimal

package webtools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

func webConfig() *Config {
	return &Config{Enabled: true, Engines: []string{"fake"}, UserAgent: "damnbot/test", policy: allowLoopback}
}

func startModule(t *testing.T, cfg *Config) *Module {
	t.Helper()
	m, err := New(testClient(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if serr := m.Start(context.Background()); serr != nil {
		t.Fatalf("Start: %v", serr)
	}
	return m
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	he, ok := errors.AsType[*host.Error](err)
	if !ok || he.Code != code {
		t.Fatalf("err = %v, want host.Error code %q", err, code)
	}
}

func TestToolsAreReadAndClosedSchema(t *testing.T) {
	t.Parallel()
	tools := Tools()
	if len(tools) != 2 {
		t.Fatalf("tools = %d", len(tools))
	}
	for _, tool := range tools {
		if tool.Name == "" || tool.Effect != "read" || len(tool.Schema) == 0 {
			t.Errorf("tool = %+v", tool)
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.Schema, &schema); err != nil {
			t.Errorf("%s schema: %v", tool.Name, err)
		}
		if schema["additionalProperties"] != false {
			t.Errorf("%s allows additional properties", tool.Name)
		}
	}
}

func TestModuleDisabledFailsClosed(t *testing.T) {
	t.Parallel()
	m, err := New(nil, &Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if serr := m.Start(context.Background()); serr != nil {
		t.Fatal(serr)
	}
	if m.Status() != host.StatusStopped {
		t.Errorf("status = %s", m.Status())
	}
	_, err = m.Invoke(context.Background(), "web_fetch", json.RawMessage(`{"url":"https://example.test"}`), host.Env{})
	wantCode(t, err, "module_unavailable")
	_, err = m.Invoke(context.Background(), "web_search", json.RawMessage(`{"query":"x"}`), host.Env{})
	wantCode(t, err, "module_unavailable")
}

func TestModuleRejectsUnknownEngine(t *testing.T) {
	t.Parallel()
	if _, err := New(nil, &Config{Engines: []string{"nope"}}); !errors.Is(err, errUnknownEngine) {
		t.Fatalf("err = %v", err)
	}
}

func TestModuleWebFetch(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := w.Write([]byte("<title>Docs</title><p>Halo <b>dunia</b></p>")); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	m := startModule(t, webConfig())
	if m.Status() != host.StatusReady {
		t.Fatalf("status = %s", m.Status())
	}
	res, err := m.Invoke(context.Background(), "web_fetch", json.RawMessage(`{"url":"`+srv.URL+`"}`), host.Env{})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(res.Content, "Halo dunia") {
		t.Errorf("content = %q", res.Content)
	}
	if res.UntrustedSource != "web:127.0.0.1" {
		t.Errorf("taint = %q", res.UntrustedSource)
	}
	if res.Meta["url"] != srv.URL || res.Meta["charset"] != "utf-8" {
		t.Errorf("meta = %v", res.Meta)
	}
}

func TestModuleWebSearch(t *testing.T) {
	t.Parallel()
	m := startModule(t, webConfig())
	res, err := m.Invoke(context.Background(), "web_search", json.RawMessage(`{"query":"golang","limit":2}`), host.Env{})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(res.Content, "Result 1") || !strings.Contains(res.Content, "Result 2") ||
		strings.Contains(res.Content, "Result 3") {
		t.Errorf("content = %q", res.Content)
	}
	if res.UntrustedSource != "web_search:fake" || res.Meta["engine"] != "fake" {
		t.Errorf("taint/meta = %q %v", res.UntrustedSource, res.Meta)
	}
}

func TestModuleSearchWithoutEngine(t *testing.T) {
	t.Parallel()
	cfg := webConfig()
	cfg.Engines = nil
	m := startModule(t, cfg)
	_, err := m.Invoke(context.Background(), "web_search", json.RawMessage(`{"query":"x"}`), host.Env{})
	wantCode(t, err, "module_unavailable")
}

func TestModuleInvalidArgsAndStop(t *testing.T) {
	t.Parallel()
	m := startModule(t, webConfig())
	for _, raw := range []string{`{}`, `{"url":"https://example.test","extra":1}`, `{"url":""}`, `not json`} {
		_, err := m.Invoke(context.Background(), "web_fetch", json.RawMessage(raw), host.Env{})
		wantCode(t, err, "invalid_args")
	}
	_, err := m.Invoke(context.Background(), "web_search", json.RawMessage(`{"query":"x","limit":21}`), host.Env{})
	wantCode(t, err, "invalid_args")
	_, err = m.Invoke(context.Background(), "nope", json.RawMessage(`{}`), host.Env{})
	wantCode(t, err, "unknown_tool")
	if serr := m.Stop(context.Background()); serr != nil || m.Status() != host.StatusStopped {
		t.Fatalf("Stop = %v status=%s", serr, m.Status())
	}
	_, err = m.Invoke(context.Background(), "web_fetch", json.RawMessage(`{"url":"https://example.test"}`), host.Env{})
	wantCode(t, err, "module_unavailable")
}
