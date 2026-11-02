//go:build !minimal

package provider_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// callbackSigner is the credential-provider seam (key_cmd, Entra, OAuth):
// the value lives in the callback, not in Route.APIKey.
type callbackSigner struct {
	mu    sync.Mutex
	value string
	calls int
}

func (s *callbackSigner) Sign(_ context.Context, req *http.Request, _ []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	req.Header.Set("Authorization", s.value)
	return nil
}

func (s *callbackSigner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestRebuiltClientKeepsCredentialCallback (EC-PROV-022): rebuilding the
// client from the same route configuration keeps Route.Auth, so a credential
// that comes from a callback (key_cmd/Entra) is still attached on every
// rebuilt client instead of silently falling back to an empty bearer.
func TestRebuiltClientKeepsCredentialCallback(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-PROV-022", "F5", "covered") {
		return
	}
	t.Run("EC-PROV-022", func(t *testing.T) {
		t.Parallel()
		var mu sync.Mutex
		var auth []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			auth = append(auth, r.Header.Get("Authorization"))
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			if _, err := io.WriteString(w, poolSSE); err != nil {
				t.Errorf("write stream: %v", err)
			}
		}))
		t.Cleanup(srv.Close)
		signer := &callbackSigner{value: "Bearer callback-token"}
		cfg := &provider.Config{Routes: []provider.Route{{
			Provider: "fake", Model: "m", BaseURL: srv.URL, Auth: signer,
		}}}
		for i := range 2 {
			o := client(t, cfg)
			if _, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil); err != nil {
				t.Fatalf("rebuilt client %d: %v", i+1, err)
			}
		}
		mu.Lock()
		got := append([]string(nil), auth...)
		mu.Unlock()
		if len(got) != 2 || got[0] != "Bearer callback-token" || got[1] != "Bearer callback-token" {
			t.Fatalf("authorization headers = %v, want the callback value twice", got)
		}
		if signer.count() != 2 {
			t.Fatalf("signer calls = %d, want 2", signer.count())
		}
	})
}
