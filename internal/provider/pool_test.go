//go:build !minimal

package provider_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/credpool"
)

const poolSSE = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n" +
	"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n" +
	"data: [DONE]\n\n"

func poolOf(t *testing.T, ids ...string) *credpool.Pool {
	t.Helper()
	creds := make([]credpool.Credential, len(ids))
	for i, id := range ids {
		creds[i] = credpool.NewCredential(id, "sk-"+id+"-secret")
	}
	p, err := credpool.New(creds)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type poolRecorder struct {
	mu     sync.Mutex
	keys   []string
	status []int
}

func (r *poolRecorder) serve(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		key := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		r.mu.Lock()
		r.keys = append(r.keys, key)
		n := len(r.keys)
		r.mu.Unlock()
		status := http.StatusOK
		if n <= len(r.status) {
			status = r.status[n-1]
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			if _, err := io.WriteString(w, `{"error":{"message":"fake","type":"invalid_request_error","code":"invalid_api_key"}}`); err != nil {
				t.Errorf("write body: %v", err)
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if _, err := io.WriteString(w, poolSSE); err != nil {
			t.Errorf("write stream: %v", err)
		}
	}
}

func (r *poolRecorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.keys...)
}

func pooledClient(t *testing.T, rec *poolRecorder, pool *credpool.Pool, events *[]provider.CredentialEvent) *provider.Pooled {
	t.Helper()
	srv := httptest.NewServer(rec.serve(t))
	t.Cleanup(srv.Close)
	base := client(t, &provider.Config{Routes: []provider.Route{route("m", srv.URL)}})
	var on func(provider.CredentialEvent)
	if events != nil {
		on = func(e provider.CredentialEvent) { *events = append(*events, e) }
	}
	p, err := provider.NewPooled(base, map[string]*credpool.Pool{"fake": pool}, on)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPoolRotatesOn401(t *testing.T) {
	t.Parallel()
	rec := &poolRecorder{status: []int{401}}
	var events []provider.CredentialEvent
	p := pooledClient(t, rec, poolOf(t, "a", "b"), &events)
	resp, err := p.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	if err != nil || resp.Content != "ok" || resp.CredentialID != "b" || resp.Attempts != 2 {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if got := rec.seen(); fmt.Sprint(got) != "[sk-a-secret sk-b-secret]" {
		t.Fatalf("keys=%v", got)
	}
	if len(events) != 1 || events[0].From != "a" || events[0].To != "b" || events[0].Reason != credpool.ReasonAuth {
		t.Fatalf("events=%+v", events)
	}
}

func TestPoolExhaustedNoExtraRequests(t *testing.T) {
	t.Parallel()
	rec := &poolRecorder{status: []int{401, 401}}
	p := pooledClient(t, rec, poolOf(t, "a", "b"), nil)
	_, err := p.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	if !errors.Is(err, credpool.ErrExhausted) || !strings.Contains(err.Error(), "credential_exhausted") {
		t.Fatalf("err=%v", err)
	}
	if len(rec.seen()) != 2 {
		t.Fatalf("requests=%v", rec.seen())
	}
	if _, err := p.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil); !errors.Is(err, credpool.ErrExhausted) {
		t.Fatalf("second err=%v", err)
	}
	if len(rec.seen()) != 2 {
		t.Fatalf("cooldown sent requests: %v", rec.seen())
	}
}

func TestPoolRotatesRateLimit(t *testing.T) {
	t.Parallel()
	rec := &poolRecorder{status: []int{429}}
	p := pooledClient(t, rec, poolOf(t, "a", "b"), nil)
	resp, err := p.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	if err != nil || resp.CredentialID != "b" {
		t.Fatalf("resp=%+v err=%v keys=%v", resp, err, rec.seen())
	}
	if got := rec.seen(); len(got) != 2 || got[0] != "sk-a-secret" || got[1] != "sk-b-secret" {
		t.Fatalf("429 did not rotate first: %v", got)
	}
}

func TestPoolDoesNotRotateNonCredential(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
		want   int
	}{
		{"400", 400, 1},
		{"503", 503, retry.MaxAttempts},
		{"408", 408, retry.MaxAttempts},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &poolRecorder{}
			for range tc.want {
				rec.status = append(rec.status, tc.status)
			}
			var events []provider.CredentialEvent
			p := pooledClient(t, rec, poolOf(t, "a", "b"), &events)
			if _, err := p.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil); err == nil {
				t.Fatal("expected error")
			}
			if len(events) != 0 {
				t.Fatalf("rotated on %d: %+v", tc.status, events)
			}
			seen := rec.seen()
			if len(seen) != tc.want {
				t.Fatalf("requests=%v", seen)
			}
			for _, k := range seen {
				if k != "sk-a-secret" {
					t.Fatalf("second credential used on %d: %v", tc.status, seen)
				}
			}
		})
	}
}

func TestPoolParallelDo(t *testing.T) {
	t.Parallel()
	p := pooledClient(t, &poolRecorder{}, poolOf(t, "a", "b", "c"), nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := p.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil); err != nil {
				t.Errorf("do: %v", err)
			}
		})
	}
	wg.Wait()
}

func TestNewPooledRejectsEmpty(t *testing.T) {
	t.Parallel()
	base := client(t, &provider.Config{Routes: []provider.Route{route("m", "http://127.0.0.1")}})
	if _, err := provider.NewPooled(base, map[string]*credpool.Pool{"fake": nil}, nil); !errors.Is(err, provider.ErrConfig) {
		t.Fatalf("err=%v", err)
	}
}
