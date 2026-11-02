//go:build !minimal

package main

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

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// F5 custom-provider acceptance: a [[providers.custom]] row is defined only in
// config, resolved by providerResolver, and replayed over loopback through the
// real openai-chat adapter. One success and one 400 prove the request body,
// credential env, extra headers/body, and model list without any provider
// code. Unsupported config fields must fail at resolution, never be ignored.

const customReplayTOML = `
[[providers.custom]]
name = "kantor"
base_url = "%s"
api = "openai-chat"
api_key_env = "KANTOR_KEY"
extra_headers = { X-Tenant = "acme" }
extra_body = { top_k = 5 }
streaming = true

[[providers.custom.models]]
id = "qwen3"
context_length = 32768
max_tokens = 2048
capabilities = ["tools", "vision"]
`

type customReplayServer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []customReplayRequest
}

type customReplayRequest struct {
	path   string
	header http.Header
	body   []byte
}

func newCustomReplayServer(t *testing.T, status int, reply string) *customReplayServer {
	t.Helper()
	s := &customReplayServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, customReplayRequest{path: r.URL.Path, header: r.Header.Clone(), body: body})
		s.mu.Unlock()
		if status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			if _, werr := io.WriteString(w, `{"error":{"message":"replay bad","type":"invalid_request_error","code":"invalid_request"}}`); werr != nil {
				return
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if _, werr := io.WriteString(w, reply); werr != nil {
			return
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *customReplayServer) take() []customReplayRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.reqs
	s.reqs = nil
	return out
}

const customReply = `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"qwen3","choices":[{"index":0,"delta":{"content":"pong"},"finish_reason":null}]}

data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"qwen3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"qwen3","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}

data: [DONE]

`

// customReplayConfig parses the TOML through the real config loader.
func customReplayConfig(t *testing.T, base string) config.Config {
	t.Helper()
	t.Setenv("KANTOR_KEY", "sk-kantor")
	l, err := config.New(config.Deps{
		ReadFile: func(string) ([]byte, error) { return nil, errors.New("unused") },
		Getenv:   func(string) string { return "" },
		HomeDir:  func() (string, error) { return t.TempDir(), nil },
		GOOS:     "linux",
	})
	if err != nil {
		t.Fatalf("config.New: %v", err)
	}
	cfg, err := l.Parse([]byte(strings.Replace(customReplayTOML, "%s", base, 1)), "config.toml")
	if err != nil {
		t.Fatalf("config parse: %v", err)
	}
	return cfg
}

func customReplayRoute(t *testing.T, cfg *config.Config, ref string) provider.Route {
	t.Helper()
	cat, err := registry.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	route, err := newResolver(cfg, registry.PriceTable{}, cat, &runOptions{}).resolve(ref)
	if err != nil {
		t.Fatalf("resolve %s: %v", ref, err)
	}
	return route
}

func customReplayAsk(route *provider.Route) *provider.Request {
	return &provider.Request{
		Model: route.Ref(), MaxTokens: 64,
		Messages: []provider.Message{{Role: "user", Content: "ping"}},
	}
}

func customReplayClient(t *testing.T, route *provider.Route) *provider.OpenAI {
	t.Helper()
	n := netx.New(netx.Config{})
	t.Cleanup(n.CloseIdleConnections)
	o, err := provider.NewOpenAI(n.Client("provider", netx.Inference), &provider.Config{
		Routes: []provider.Route{*route}, UserAgent: "damnbot/replay",
		Wait: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	return o
}

func TestCustomProviderReplaySuccess(t *testing.T) {
	t.Setenv("KANTOR_KEY", "sk-kantor")
	srv := newCustomReplayServer(t, http.StatusOK, customReply)
	cfg := customReplayConfig(t, srv.srv.URL)
	route := customReplayRoute(t, &cfg, "kantor/qwen3")
	checkCustomRoute(t, &route, srv.srv.URL)
	resp, err := customReplayClient(t, &route).Do(t.Context(), provider.PurposeTurn, customReplayAsk(&route), nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Content != "pong" || resp.FinishReason != provider.FinishStop || resp.Usage.Input != 5 {
		t.Fatalf("response = %+v", resp)
	}
	seen := srv.take()
	if len(seen) != 1 {
		t.Fatalf("%d requests, want 1", len(seen))
	}
	checkCustomRequest(t, seen[0])
}

func checkCustomRoute(t *testing.T, route *provider.Route, base string) {
	t.Helper()
	if route.APIKey != "sk-kantor" || route.BaseURL != base ||
		len(route.Protocols) != 1 || route.Protocols[0] != registry.ProtocolOpenAIChat {
		t.Fatalf("route = %+v", route)
	}
	if route.Headers["X-Tenant"] != "acme" || route.ExtraBody["top_k"] != int64(5) {
		t.Fatalf("headers/body = %v/%v", route.Headers, route.ExtraBody)
	}
}

func checkCustomRequest(t *testing.T, req customReplayRequest) {
	t.Helper()
	if req.path != "/chat/completions" || req.header.Get("Authorization") != "Bearer sk-kantor" ||
		req.header.Get("X-Tenant") != "acme" {
		t.Fatalf("request path/headers = %s/%v", req.path, req.header)
	}
	var body map[string]any
	if uerr := json.Unmarshal(req.body, &body); uerr != nil {
		t.Fatalf("body %s: %v", req.body, uerr)
	}
	if body["model"] != "qwen3" || body["top_k"] != float64(5) || body["stream"] != true {
		t.Fatalf("body = %s", req.body)
	}
	if !strings.Contains(string(req.body), `"content":"ping"`) {
		t.Fatalf("body lacks the user turn: %s", req.body)
	}
}

func TestCustomProviderReplayError(t *testing.T) {
	t.Setenv("KANTOR_KEY", "sk-kantor")
	srv := newCustomReplayServer(t, http.StatusBadRequest, "")
	cfg := customReplayConfig(t, srv.srv.URL)
	route := customReplayRoute(t, &cfg, "kantor/qwen3")
	_, err := customReplayClient(t, &route).Do(t.Context(), provider.PurposeTurn, customReplayAsk(&route), nil)
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Class != retry.BadRequest || pe.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("error = %v", err)
	}
	if n := len(srv.take()); n != 1 {
		t.Fatalf("%d requests, want 1 (400 is not retried)", n)
	}
}

// TestCustomProviderRejectsUnwiredFields: fields the runtime cannot honor are
// startup errors, so the 12-field schema is never a silent no-op.
func TestCustomProviderRejectsUnwiredFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		edit func(*config.CustomProvider)
		want string
	}{
		{"tls", func(c *config.CustomProvider) { c.TLS = &config.CustomTLS{} }, "tls is not wired"},
		{"streaming false", func(c *config.CustomProvider) { v := false; c.Streaming = &v }, "streaming = false"},
		{"session affinity", func(c *config.CustomProvider) { c.SessionAffinityHeader = "X-Session" }, "session_affinity_header is not wired"},
		{"key_cmd", func(c *config.CustomProvider) { c.KeyCmd = []string{"sh", "-c", "echo k"} }, "needs the F5-03 auth transport"},
		{"api_key reference", func(c *config.CustomProvider) { c.APIKey = "keychain:kantor" }, "keychain reference"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.Default()
			entry := config.CustomProvider{
				Name: "kantor", BaseURL: "https://kantor.example.com/v1", API: "openai-chat",
				APIKeyEnv: "KANTOR_KEY", Models: []config.CustomModel{{ID: "qwen3"}},
			}
			c.edit(&entry)
			cfg.Providers.Custom = []config.CustomProvider{entry}
			_, err := resolveTest(t, &cfg, nil, "kantor/qwen3")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want %q", err, c.want)
			}
		})
	}
}
