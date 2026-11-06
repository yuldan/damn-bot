package gwapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const fixtureValue = "local-test"

// fakeEngine records turns and replays a scripted update stream.
type fakeEngine struct {
	run func(context.Context, *Turn) (<-chan Update, error)
}

func (f *fakeEngine) Run(ctx context.Context, turn *Turn) (<-chan Update, error) {
	if f.run != nil {
		return f.run(ctx, turn)
	}
	return scripted(Update{Kind: UpdateText, Text: "hello"}, Update{Kind: UpdateStop, FinishReason: "stop"}), nil
}

func scripted(ups ...Update) <-chan Update {
	ch := make(chan Update, len(ups))
	for i := range ups {
		ch <- ups[i]
	}
	close(ch)
	return ch
}

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

func get(t *testing.T, h http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func authHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + fixtureValue}
}
