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
	"github.com/damnbot/damnbot/internal/testutil/phase"
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
	body   string
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
			body := r.body
			if body == "" {
				body = `{"error":{"message":"fake","type":"invalid_request_error","code":"invalid_api_key"}}`
			}
			if _, err := io.WriteString(w, body); err != nil {
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

// TestPoolRotationKeepsRouteHost (EC-PROV-017): the credential pool belongs
// to a provider, not to a host. Rotating on one route keeps every request on
// that route's base URL; a later route of the same provider rotates again but
// still sends to its own host, so a session never jumps endpoints.
func TestPoolRotationKeepsRouteHost(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-PROV-017", "F5", "covered") {
		return
	}
	t.Run("EC-PROV-017", func(t *testing.T) {
		t.Parallel()
		recA := &poolRecorder{status: []int{http.StatusUnauthorized}}
		recB := &poolRecorder{}
		srvA := httptest.NewServer(recA.serve(t))
		t.Cleanup(srvA.Close)
		srvB := httptest.NewServer(recB.serve(t))
		t.Cleanup(srvB.Close)
		base := client(t, &provider.Config{Routes: []provider.Route{
			route("a", srvA.URL),
			route("b", srvB.URL),
		}})
		p, err := provider.NewPooled(base, map[string]*credpool.Pool{"fake": poolOf(t, "a1", "a2")}, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := p.Do(t.Context(), provider.PurposeTurn, ask("fake/a"), nil)
		if err != nil || resp.CredentialID != "a2" {
			t.Fatalf("route a: resp=%+v err=%v", resp, err)
		}
		if got := recA.seen(); fmt.Sprint(got) != "[sk-a1-secret sk-a2-secret]" {
			t.Fatalf("route a keys = %v", got)
		}
		if got := recB.seen(); len(got) != 0 {
			t.Fatalf("rotation leaked to the other host: %v", got)
		}
		resp, err = p.Do(t.Context(), provider.PurposeTurn, ask("fake/b"), nil)
		if err != nil || resp.CredentialID != "a2" {
			t.Fatalf("route b: resp=%+v err=%v", resp, err)
		}
		if got := recB.seen(); fmt.Sprint(got) != "[sk-a2-secret]" {
			t.Fatalf("route b keys = %v", got)
		}
		if got := recA.seen(); len(got) != 2 {
			t.Fatalf("route a received an extra request: %v", got)
		}
	})
}

// TestBillingAmbiguous400Rotates (EC-PROV-020): a 400 that names an exhausted
// billing/extra-usage budget is ambiguous, so the pool rotates the credential
// with the short billing cooldown instead of freezing it. A plain 400
// bad_request still stays on the current key, and a 429/401 keeps its own
// reason even when the body carries billing wording.
func TestBillingAmbiguous400Rotates(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-PROV-020", "F5", "covered") {
		return
	}
	t.Run("EC-PROV-020", func(t *testing.T) {
		t.Parallel()
		cases := []poolBillingCase{
			{"out-of-extra-usage", http.StatusBadRequest, `{"error":{"message":"You are out of extra usage. Add credits.","type":"invalid_request_error","code":"out_of_extra_usage"}}`, credpool.ReasonBilling, true},
			{"plain-bad-request", http.StatusBadRequest, `{"error":{"message":"invalid request","type":"invalid_request_error","code":"invalid_request"}}`, "", false},
			{"rate-limit-wins", http.StatusTooManyRequests, `{"error":{"message":"out of extra usage","type":"rate_limit_error"}}`, credpool.ReasonRateLimited, true},
			{"auth-wins", http.StatusUnauthorized, `{"error":{"message":"out of extra usage","type":"authentication_error"}}`, credpool.ReasonAuth, true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				checkPoolBillingCase(t, tc)
			})
		}
	})
}

type poolBillingCase struct {
	name   string
	status int
	body   string
	reason credpool.Reason
	rotate bool
}

// checkPoolBillingCase runs one EC-PROV-020 classification through the pool.
func checkPoolBillingCase(t *testing.T, tc poolBillingCase) {
	t.Helper()
	rec := &poolRecorder{status: []int{tc.status}, body: tc.body}
	var events []provider.CredentialEvent
	p := pooledClient(t, rec, poolOf(t, "a", "b"), &events)
	resp, err := p.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	if !tc.rotate {
		if err == nil || len(events) != 0 || len(rec.seen()) != 1 {
			t.Fatalf("plain 400 rotated or succeeded: resp=%+v err=%v events=%+v keys=%v",
				resp, err, events, rec.seen())
		}
		return
	}
	if err != nil || resp.CredentialID != "b" {
		t.Fatalf("resp=%+v err=%v keys=%v", resp, err, rec.seen())
	}
	if len(events) != 1 || events[0].Reason != tc.reason {
		t.Fatalf("events=%+v, want reason %s", events, tc.reason)
	}
	if tc.reason == credpool.ReasonBilling && events[0].Cooldown != credpool.DefaultBillingCooldown {
		t.Fatalf("billing cooldown=%s, want %s", events[0].Cooldown, credpool.DefaultBillingCooldown)
	}
}
