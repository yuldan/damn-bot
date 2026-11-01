package provider_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestOAuthBearerHostPinned (EC-SEC-004): the bearer reaches the configured
// host, and a cross-host redirect is not followed, so a third-party host never
// receives it.
func TestOAuthBearerHostPinned(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-SEC-004", "F5", "covered") {
		return
	}
	var foreign atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		foreign.Add(1)
	}))
	t.Cleanup(evil.Close)
	var gotAuth atomic.Value
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		http.Redirect(w, r, evil.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	o := client(t, &provider.Config{Routes: []provider.Route{route("m", redirector.URL)}})
	if _, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil); err == nil {
		t.Fatal("cross-host redirect: want the 3xx surfaced as an error")
	}
	got, ok := gotAuth.Load().(string)
	if !ok || got != "Bearer sk-test" {
		t.Fatalf("configured host auth = %q, want the bearer", got)
	}
	if n := foreign.Load(); n != 0 {
		t.Fatalf("foreign host received %d requests, want 0", n)
	}
}
