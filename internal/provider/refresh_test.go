package provider_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/auth"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

type rotatingSigner struct{ token atomic.Pointer[string] }

type refreshAuth struct {
	*rotatingSigner
	ref *auth.CooldownRefresher
}

func (r *refreshAuth) Epoch() uint64 { return r.ref.Epoch() }

func (r *refreshAuth) Refresh(ctx context.Context, since uint64) error {
	if err := r.ref.Refresh(ctx, since); err != nil {
		return fmt.Errorf("refresh: %w", err)
	}
	return nil
}

func newRotatingSigner() *rotatingSigner {
	s := &rotatingSigner{}
	bad := "bad"
	s.token.Store(&bad)
	return s
}

func (s *rotatingSigner) Sign(_ context.Context, req *http.Request, _ []byte) error {
	req.Header.Set("Authorization", "Bearer "+*s.token.Load())
	return nil
}

func (s *rotatingSigner) set(token string) { s.token.Store(&token) }

// refreshServer accepts only "good"; every other bearer is handled by the
// fixed status, so a 403 can never be mistaken for a refresh trigger.
func refreshServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
			writeBody(t, w, `{"error":{"message":"no","type":"auth"}}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			writeBody(t, w, `{"error":{"message":"expired","type":"auth"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeBody(t, w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n"+
			"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeBody(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := w.Write([]byte(body)); err != nil {
		t.Errorf("write response: %v", err)
	}
}

func askRefresh() *provider.Request {
	return &provider.Request{Model: "fake/m", Messages: []provider.Message{{Role: "user", Content: "hi"}}}
}

// TestRefreshAfter401 (EC-PROV-014): one 401 triggers exactly one refresh and
// one retry; the retried request succeeds.
func TestRefreshAfter401(t *testing.T) {
	g := phase.Current(t)
	if !g.Item(t, "EC-PROV-014", "F5", "covered") {
		return
	}
	t.Run("EC-PROV-014", func(t *testing.T) {
		srv := refreshServer(t, 0)
		signer := newRotatingSigner()
		var calls atomic.Int32
		refresher := auth.NewCooldownRefresher(func(context.Context) error {
			calls.Add(1)
			signer.set("good")
			return nil
		}, time.Minute)
		rt := provider.Route{
			Provider: "fake", Model: "m", BaseURL: srv.URL,
			Auth: &refreshAuth{rotatingSigner: signer, ref: refresher}, ContextWindow: 128_000,
		}
		o := client(t, &provider.Config{Routes: []provider.Route{rt}})
		resp, err := o.Do(t.Context(), provider.PurposeTurn, askRefresh(), nil)
		if err != nil || resp.Content != "ok" || calls.Load() != 1 {
			t.Fatalf("resp=%+v err=%v refresh=%d", resp, err, calls.Load())
		}
	})
}

// TestRefreshSecond401Denied: a 401 after the refresh is a denied request,
// not another refresh.
func TestRefreshSecond401Denied(t *testing.T) {
	srv := refreshServer(t, http.StatusUnauthorized)
	signer := newRotatingSigner()
	var calls atomic.Int32
	refresher := auth.NewCooldownRefresher(func(context.Context) error {
		calls.Add(1)
		signer.set("still-bad")
		return nil
	}, time.Minute)
	rt := provider.Route{Provider: "fake", Model: "m", BaseURL: srv.URL, Auth: &refreshAuth{rotatingSigner: signer, ref: refresher}}
	o := client(t, &provider.Config{Routes: []provider.Route{rt}})
	_, err := o.Do(t.Context(), provider.PurposeTurn, askRefresh(), nil)
	if !errors.Is(err, provider.ErrAuthDenied) || calls.Load() != 1 {
		t.Fatalf("err=%v refresh=%d", err, calls.Load())
	}
}

// TestRefreshNotOn403: entitlement 403 must not spend a refresh.
func TestRefreshNotOn403(t *testing.T) {
	srv := refreshServer(t, http.StatusForbidden)
	var calls atomic.Int32
	refresher := auth.NewCooldownRefresher(func(context.Context) error {
		calls.Add(1)
		return nil
	}, time.Minute)
	rt := provider.Route{
		Provider: "fake", Model: "m", BaseURL: srv.URL,
		Auth: &refreshAuth{rotatingSigner: newRotatingSigner(), ref: refresher},
	}
	o := client(t, &provider.Config{Routes: []provider.Route{rt}})
	if _, err := o.Do(t.Context(), provider.PurposeTurn, askRefresh(), nil); err == nil {
		t.Fatal("403 succeeded")
	}
	if calls.Load() != 0 {
		t.Fatalf("403 triggered %d refreshes", calls.Load())
	}
}

// TestRefreshParallelSingleFlight: ten parallel 401s share one refresh under
// the race detector.
func TestRefreshParallelSingleFlight(t *testing.T) {
	srv := refreshServer(t, 0)
	signer := newRotatingSigner()
	var calls atomic.Int32
	refresher := auth.NewCooldownRefresher(func(context.Context) error {
		calls.Add(1)
		signer.set("good")
		return nil
	}, time.Minute)
	rt := provider.Route{Provider: "fake", Model: "m", BaseURL: srv.URL, Auth: &refreshAuth{rotatingSigner: signer, ref: refresher}}
	o := client(t, &provider.Config{Routes: []provider.Route{rt}})
	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := range 10 {
		wg.Go(func() {
			_, errs[i] = o.Do(t.Context(), provider.PurposeTurn, askRefresh(), nil)
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("refreshes = %d, want 1", calls.Load())
	}
}

// TestRefreshErrorHidesToken: a failing refresher's cause never reaches the
// error text.
func TestRefreshErrorHidesToken(t *testing.T) {
	srv := refreshServer(t, http.StatusUnauthorized)
	refresher := auth.NewCooldownRefresher(func(context.Context) error {
		return errors.New("refresh-TOKEN-SECRET")
	}, time.Minute)
	rt := provider.Route{
		Provider: "fake", Model: "m", BaseURL: srv.URL,
		Auth: &refreshAuth{rotatingSigner: newRotatingSigner(), ref: refresher},
	}
	o := client(t, &provider.Config{Routes: []provider.Route{rt}})
	_, err := o.Do(t.Context(), provider.PurposeTurn, askRefresh(), nil)
	if !errors.Is(err, provider.ErrRefreshFailed) || strings.Contains(err.Error(), "TOKEN-SECRET") {
		t.Fatalf("err = %v", err)
	}
}
