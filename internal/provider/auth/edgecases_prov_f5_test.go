//go:build !minimal

package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// refreshFixture is a token endpoint that rotates refresh-1 -> refresh-2 ->
// refresh-3 and rejects anything else, so replaying an older value fails.
type refreshFixture struct {
	mu   sync.Mutex
	seen []string
	srv  *httptest.Server
}

func newRefreshFixture(t *testing.T) *refreshFixture {
	t.Helper()
	f := &refreshFixture{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *refreshFixture) serve(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	refresh := r.Form.Get("refresh_token")
	f.mu.Lock()
	f.seen = append(f.seen, refresh)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	var body string
	switch refresh {
	case "refresh-1":
		body = `{"access_token":"access-2","refresh_token":"refresh-2","token_type":"Bearer","expires_in":3600}`
	case "refresh-2":
		body = `{"access_token":"access-3","refresh_token":"refresh-3","token_type":"Bearer","expires_in":3600}`
	default:
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	if _, err := io.WriteString(w, body); err != nil {
		http.Error(w, `{"error":"write_failed"}`, http.StatusInternalServerError)
	}
}

func (f *refreshFixture) tokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

// TestRefreshTokenNeverReplayed (EC-PROV-019): after a refresh rotates the
// refresh token, the next refresh loads the stored token and sends the new
// value. Replaying the old value is rejected by the server, so a stale
// singleton cannot pass the second refresh.
func TestRefreshTokenNeverReplayed(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-PROV-019", "F5", "covered") {
		return
	}
	t.Run("EC-PROV-019", func(t *testing.T) {
		t.Parallel()
		f := newRefreshFixture(t)
		store := fileStore(t)
		now := func() time.Time { return time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC) }
		tok := Token{
			Provider:      "fake",
			AccessToken:   "access-1",
			RefreshToken:  "refresh-1",
			TokenEndpoint: f.srv.URL,
			ClientID:      "client",
		}
		if err := store.Save(t.Context(), &tok); err != nil {
			t.Fatal(err)
		}
		o, err := NewOAuth(&OAuthConfig{Provider: "fake", Store: store, Client: testClient(), Now: now})
		if err != nil {
			t.Fatal(err)
		}
		refresher := o.Refresher(0)
		for i := range 2 {
			if rerr := refresher.Refresh(t.Context(), refresher.Epoch()); rerr != nil {
				t.Fatalf("refresh %d: %v", i+1, rerr)
			}
		}
		if got, want := f.tokens(), []string{"refresh-1", "refresh-2"}; !slices.Equal(got, want) {
			t.Fatalf("refresh tokens sent = %v, want %v", got, want)
		}
		stored, err := store.Load(t.Context(), "fake")
		if err != nil || stored.RefreshToken != "refresh-3" {
			t.Fatalf("stored token = %+v, %v", stored, err)
		}
		// Negative control: a third refresh must use the newest token; the
		// server rejects anything else (including a replayed refresh-1).
		if rerr := refresher.Refresh(context.Background(), refresher.Epoch()); rerr == nil {
			t.Fatal("refresh with the newest token should be rejected by this fixture")
		}
		if got := f.tokens(); len(got) != 3 || got[2] != "refresh-3" {
			t.Fatalf("third refresh sent %v, want refresh-3", got)
		}
	})
}

// testRefresher stores one token behind endpoint and returns its refresher.
func testRefresher(t *testing.T, endpoint, refresh string) *CooldownRefresher {
	t.Helper()
	store := fileStore(t)
	tok := Token{
		Provider: "fake", AccessToken: "access-1", RefreshToken: refresh,
		TokenEndpoint: endpoint, ClientID: "client",
	}
	if err := store.Save(t.Context(), &tok); err != nil {
		t.Fatal(err)
	}
	o, err := NewOAuth(&OAuthConfig{
		Provider: "fake", Store: store, Client: testClient(),
		Now: func() time.Time { return time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return o.Refresher(0)
}

// TestInvalidGrantMarksRefresherDead (EC-PROV-018): a revoked/expired grant
// is permanent, so the refresher reports DEAD and never calls the token
// endpoint again. A transient 500 is not DEAD: it sets a cooldown and may
// retry later.
func TestInvalidGrantMarksRefresherDead(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-PROV-018", "F5", "covered") {
		return
	}
	t.Run("EC-PROV-018", func(t *testing.T) {
		t.Parallel()
		f := newRefreshFixture(t)
		refresher := testRefresher(t, f.srv.URL, "revoked")
		for i := range 2 {
			if err := refresher.Refresh(t.Context(), refresher.Epoch()); !errors.Is(err, ErrInvalidGrant) {
				t.Fatalf("refresh %d = %v, want ErrInvalidGrant", i+1, err)
			}
		}
		if !refresher.Dead() {
			t.Fatal("refresher is not DEAD after invalid_grant")
		}
		if got := f.tokens(); len(got) != 1 {
			t.Fatalf("dead refresher called the token endpoint %d times, want 1: %v", len(got), got)
		}
		// Negative control: a transient failure keeps the cooldown path and
		// is not promoted to DEAD.
		var calls atomic.Int32
		flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			http.Error(w, `{"error":"server_error"}`, http.StatusInternalServerError)
		}))
		t.Cleanup(flaky.Close)
		transient := testRefresher(t, flaky.URL, "refresh-1")
		if err := transient.Refresh(t.Context(), transient.Epoch()); !errors.Is(err, ErrRefreshFailed) {
			t.Fatalf("transient refresh = %v, want ErrRefreshFailed", err)
		}
		if transient.Dead() {
			t.Fatal("transient failure marked the refresher DEAD")
		}
		if err := transient.Refresh(t.Context(), transient.Epoch()); !errors.Is(err, ErrRefreshCooldown) {
			t.Fatalf("cooled refresh = %v, want ErrRefreshCooldown", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("cooldown called the token endpoint %d times, want 1", calls.Load())
		}
	})
}
