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

// tokenCounter is a token endpoint that always fails with one status/body and
// counts requests, so a dead credential can be proven not to touch it again.
func tokenCounter(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, body, status)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// TestRefreshInvalidGrantDead (EC-PROV-018): invalid_grant revokes the grant,
// so the credential is DEAD permanently: refresh returns ErrRefreshDead, the
// token endpoint is never called again, and no cooldown reopens it.
func TestRefreshInvalidGrantDead(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-PROV-018", "F5", "covered") {
		return
	}
	t.Run("EC-PROV-018", func(t *testing.T) {
		t.Parallel()
		srv, calls := tokenCounter(t, http.StatusBadRequest, `{"error":"invalid_grant"}`)
		store := fileStore(t)
		now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
		tok := Token{Provider: "fake", AccessToken: "access-1", RefreshToken: "refresh-1", TokenEndpoint: srv.URL, ClientID: "client"}
		if err := store.Save(t.Context(), &tok); err != nil {
			t.Fatal(err)
		}
		o, err := NewOAuth(&OAuthConfig{Provider: "fake", Store: store, Client: testClient(), Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		r := o.Refresher(0)
		r.SetClock(func() time.Time { return now })
		rerr := r.Refresh(t.Context(), r.Epoch())
		if !errors.Is(rerr, ErrRefreshDead) || !errors.Is(rerr, ErrRefreshFailed) {
			t.Fatalf("first refresh = %v, want dead", rerr)
		}
		if !r.Dead() || calls.Load() != 1 {
			t.Fatalf("dead=%t calls=%d, want dead after one request", r.Dead(), calls.Load())
		}
		// A day later the state must still be dead; a cooldown would have
		// reopened and touched the endpoint again.
		now = now.Add(24 * time.Hour)
		if again := r.Refresh(t.Context(), r.Epoch()); !errors.Is(again, ErrRefreshDead) {
			t.Fatalf("later refresh = %v, want dead", again)
		}
		if errors.Is(rerr, ErrRefreshCooldown) {
			t.Fatal("dead credential entered cooldown")
		}
		if calls.Load() != 1 || !r.Dead() {
			t.Fatalf("dead credential re-contacted endpoint: calls=%d", calls.Load())
		}
	})
}

// TestRefreshOrdinary401Cooldown: a plain 401 is not invalid_grant; the
// credential stays alive and the refresher applies the regular 30 s cooldown.
func TestRefreshOrdinary401Cooldown(t *testing.T) {
	t.Parallel()
	srv, calls := tokenCounter(t, http.StatusUnauthorized, `{"error":"unauthorized"}`)
	store := fileStore(t)
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	tok := Token{Provider: "fake", AccessToken: "access-1", RefreshToken: "refresh-1", TokenEndpoint: srv.URL, ClientID: "client"}
	if err := store.Save(t.Context(), &tok); err != nil {
		t.Fatal(err)
	}
	o, err := NewOAuth(&OAuthConfig{Provider: "fake", Store: store, Client: testClient(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	r := o.Refresher(0)
	r.SetClock(func() time.Time { return now })
	if rerr := r.Refresh(t.Context(), r.Epoch()); !errors.Is(rerr, ErrRefreshFailed) || errors.Is(rerr, ErrRefreshDead) {
		t.Fatalf("first refresh = %v, want ordinary failure", rerr)
	}
	if rerr := r.Refresh(t.Context(), r.Epoch()); !errors.Is(rerr, ErrRefreshCooldown) {
		t.Fatalf("second refresh = %v, want cooldown", rerr)
	}
	if calls.Load() != 1 || r.Dead() {
		t.Fatalf("401 marked dead or retried early: calls=%d dead=%t", calls.Load(), r.Dead())
	}
	now = now.Add(30 * time.Second)
	if rerr := r.Refresh(t.Context(), r.Epoch()); !errors.Is(rerr, ErrRefreshFailed) || calls.Load() != 2 || r.Dead() {
		t.Fatalf("after cooldown = %v calls=%d dead=%t", rerr, calls.Load(), r.Dead())
	}
}
