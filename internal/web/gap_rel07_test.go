package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestF8WebProviderStatus: the web status stays quiet for 9 s and shows
// "waiting for provider" once the REL-07 threshold is reached.
func TestF8WebProviderStatus(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	wait := 9 * time.Second
	srv, ts := newTestServer(t, func(c *Config) {
		c.ProviderWait = func() time.Duration {
			mu.Lock()
			defer mu.Unlock()
			return wait
		}
	})
	session, _ := signIn(t, srv, ts)
	read := func() map[string]any {
		t.Helper()
		res := get(t, ts, "/api/session", session)
		if res.status != http.StatusOK {
			t.Fatalf("session = %d", res.status)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(res.body), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	if body := read(); body["waiting_provider_s"] != nil {
		t.Fatalf("9 s already reports a wait: %v", body)
	}
	mu.Lock()
	wait = 12 * time.Second
	mu.Unlock()
	if body := read(); body["waiting_provider_s"] != float64(12) {
		t.Fatalf("12 s = %v", body)
	}
	if got := get(t, ts, "/", session).body; !containsAll(got, "provider-state", "data-msg-waiting-provider") {
		t.Fatalf("dashboard lacks the provider status surface")
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
