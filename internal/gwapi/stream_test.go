package gwapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestAPIServerStreamCancel proves a cancelled request cancels the turn and
// still drains the engine channel so the loop goroutine can close.
func TestAPIServerStreamCancel(t *testing.T) {
	t.Parallel()
	cancelled := make(chan struct{})
	release := make(chan struct{})
	var runs atomic.Int32
	eng := &fakeEngine{run: func(ctx context.Context, _ *Turn) (<-chan Update, error) {
		runs.Add(1)
		ch := make(chan Update, 1)
		ch <- Update{Kind: UpdateText, Text: "first"}
		close(release)
		go func() {
			<-ctx.Done()
			close(cancelled)
			close(ch)
		}()
		return ch, nil
	}}
	srv := newTestServer(t, eng, nil, testConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions",
		strings.NewReader(chatterBody(`"stream":true`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+fixtureValue)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.Handler().ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-release:
	case <-time.After(2 * time.Second):
		t.Fatal("engine never started")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("engine context was not cancelled")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not drain and return after cancel")
	}
	if runs.Load() != 1 {
		t.Fatalf("engine runs = %d", runs.Load())
	}
	if strings.Contains(rec.Body.String(), "[DONE]") {
		t.Errorf("cancelled stream must not finish: %q", rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("stream did not start: %q", rec.Body.String())
	}
}
