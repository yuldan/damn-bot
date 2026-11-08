package connectors

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

func TestF8TOOL088XSearch(t *testing.T) {
	t.Parallel()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer xai-key-123456" {
			t.Errorf("authorization = %q", got)
		}
		decodeJSON(t, r.Body, &body)
		writeBody(t, w, `{"choices":[{"message":{"content":"a post"}}],"citations":["https://x.com/i/1"]}`)
	}))
	defer srv.Close()
	raw, err := json.Marshal(map[string]any{"enabled": true, "key_env": "XAI_KEY", "base_url": srv.URL})
	if err != nil {
		t.Fatalf("x_search raw: %v", err)
	}
	cfg := Config{Enabled: true, Timeout: 5 * time.Second, XSearch: raw}
	m := New(&cfg, Deps{Secrets: testSecrets, Now: time.Now})
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	res, ierr := m.Invoke(t.Context(), "x_search", json.RawMessage(`{"query":"golang","limit":3}`), host.Env{})
	if ierr != nil {
		t.Fatalf("search: %v", ierr)
	}
	data := mustJSON(t, body)
	if !strings.Contains(data, `"max_search_results":3`) || !strings.Contains(data, `"type":"x"`) || !strings.Contains(data, "golang") {
		t.Fatalf("request body = %v", body)
	}
	if !strings.Contains(res.Content, "a post") || res.UntrustedSource != "connector:xsearch" {
		t.Fatalf("result = %+v", res)
	}
	if _, err := m.Invoke(t.Context(), "x_search", json.RawMessage(`{"query":"x","limit":21}`), host.Env{}); err == nil {
		t.Fatal("limit 21 was accepted")
	}
}
