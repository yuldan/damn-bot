package gwapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const fixtureValue = "local-test"

// fakeEngine records turns and replays a scripted update stream.
type fakeEngine struct {
	mu    sync.Mutex
	turns []Turn
	run   func(context.Context, *Turn) (<-chan Update, error)
}

func (f *fakeEngine) Run(ctx context.Context, turn *Turn) (<-chan Update, error) {
	f.mu.Lock()
	f.turns = append(f.turns, *turn)
	f.mu.Unlock()
	if f.run != nil {
		return f.run(ctx, turn)
	}
	return scripted(Update{Kind: UpdateText, Text: "hello"}, stopUpdate("stop")), nil
}

func (f *fakeEngine) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.turns)
}

func (f *fakeEngine) last() Turn {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.turns) == 0 {
		return Turn{}
	}
	return f.turns[len(f.turns)-1]
}

func scripted(ups ...Update) <-chan Update {
	ch := make(chan Update, len(ups))
	for i := range ups {
		ch <- ups[i]
	}
	close(ch)
	return ch
}

func stopUpdate(finish string) Update {
	return Update{Kind: UpdateStop, FinishReason: finish, Usage: &Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}}
}

type fakeToolbox []ToolSpec

func (f fakeToolbox) Tools() []ToolSpec { return f }

func testConfig() *Config {
	return &Config{
		APIKey:    fixtureValue,
		Models:    []string{"damnbot"},
		BodyLimit: 1 << 20,
		Timeout:   5 * time.Second,
		MaxTokens: 4096,
		Clock:     func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID:     func() string { return "chatcmpl-test" },
	}
}

func newTestServer(t *testing.T, eng Engine, tb Toolbox, cfg *Config) *Server {
	t.Helper()
	srv, err := NewServer(cfg, eng, tb)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

func request(t *testing.T, method, path, body string, headers map[string]string) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func postJSON(t *testing.T, h http.Handler, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request(t, http.MethodPost, "/v1/chat/completions", body, headers))
	return rec
}

func get(t *testing.T, h http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request(t, http.MethodGet, path, "", headers))
	return rec
}

func authHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + fixtureValue}
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) ErrorResponse {
	t.Helper()
	var er ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &er); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return er
}

func chatterBody(extra string) string {
	b := `{"model":"damnbot","messages":[{"role":"user","content":"hi"}]`
	if extra != "" {
		b += "," + extra
	}
	return b + "}"
}
