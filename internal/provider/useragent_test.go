package provider_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/damnbot/damnbot/internal/provider"
)

const userAgentSSE = `data: {"choices":[{"index":0,"delta":{"content":"ok"}}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}

data: [DONE]

`

// captureServer records the headers of the one inference request and replays
// a minimal SSE reply.
func captureServer(t *testing.T, seen *http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		if _, err := w.Write([]byte(userAgentSSE)); err != nil {
			t.Errorf("write reply: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestUserAgentDefault pins SEC-25: every request carries "damnbot/<versi>",
// never the Go default or an OS/hostname string.
func TestUserAgentDefault(t *testing.T) {
	t.Parallel()
	var got http.Header
	srv := captureServer(t, &got)
	o := client(t, &provider.Config{
		Routes:    []provider.Route{route("m", srv.URL+"/v1")},
		UserAgent: "damnbot/9.9.9",
	})
	if _, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ua := got.Get("User-Agent"); ua != "damnbot/9.9.9" {
		t.Fatalf("User-Agent = %q, want damnbot/9.9.9", ua)
	}

	var fallback http.Header
	srv2 := captureServer(t, &fallback)
	o2 := client(t, &provider.Config{Routes: []provider.Route{route("m", srv2.URL+"/v1")}})
	if _, err := o2.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ua := fallback.Get("User-Agent"); ua != provider.DefaultUserAgent {
		t.Fatalf("default User-Agent = %q, want %q", ua, provider.DefaultUserAgent)
	}
	for _, h := range []string{"HTTP-Referer", "X-Title", "X-Client-Name"} {
		if v := fallback.Get(h); v != "" {
			t.Errorf("default request carries attribution header %s: %q", h, v)
		}
	}
}
