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
	"github.com/damnbot/damnbot/internal/provider/auth"
	"github.com/damnbot/damnbot/internal/provider/credpool"
)

const sseOK = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n" +
	"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n" +
	"data: [DONE]\n\n"

func credentialPool(t *testing.T, ids ...string) *credpool.Pool {
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

// keyRecorder is a loopback provider that records the bearer of every
// request and answers with statuses[key index] (0 = a 200 SSE stream).
type keyRecorder struct {
	mu     sync.Mutex
	keys   []string
	status []int
}

func (k *keyRecorder) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		k.mu.Lock()
		k.keys = append(k.keys, key)
		n := len(k.keys)
		k.mu.Unlock()
		status := 200
		if n <= len(k.status) {
			status = k.status[n-1]
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			body := `{"error":{"message":"fake","type":"invalid_request_error","code":"invalid_api_key"}}`
			if _, err := io.WriteString(w, body); err != nil {
				t.Errorf("write error body: %v", err)
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if _, err := io.WriteString(w, sseOK); err != nil {
			t.Errorf("write stream: %v", err)
		}
	}
}

func (k *keyRecorder) seen() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.keys...)
}

func poolClient(t *testing.T, rec *keyRecorder, pool *credpool.Pool, events *[]provider.CredentialEvent) *provider.OpenAI {
	t.Helper()
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)
	rt := route("m", srv.URL)
	provider.WithCredentialPool(&rt, pool)
	cfg := &provider.Config{Routes: []provider.Route{rt}}
	if events != nil {
		cfg.OnCredential = func(e provider.CredentialEvent) { *events = append(*events, e) }
	}
	return client(t, cfg)
}

func TestCredentialPoolRotatesOn401(t *testing.T) {
	t.Parallel()
	rec := &keyRecorder{status: []int{401}}
	var events []provider.CredentialEvent
	o := poolClient(t, rec, credentialPool(t, "a", "b"), &events)
	resp, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" || resp.CredentialID != "b" || resp.Attempts != 2 {
		t.Fatalf("response %+v", resp)
	}
	if got := rec.seen(); len(got) != 2 || got[0] != "sk-a-secret" || got[1] != "sk-b-secret" {
		t.Fatalf("keys = %v", got)
	}
	checkRotationEvent(t, events)
}

func checkRotationEvent(t *testing.T, events []provider.CredentialEvent) {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	e := events[0]
	if e.Provider != "fake" || e.From != "a" || e.To != "b" || e.Reason != credpool.ReasonAuth || e.Cooldown == 0 || e.Attempt != 2 {
		t.Fatalf("event = %+v", e)
	}
	if strings.Contains(fmt.Sprintf("%+v", e), "secret") {
		t.Fatalf("event leaked a value: %+v", e)
	}
}

func TestCredentialPoolExhausted(t *testing.T) {
	t.Parallel()
	rec := &keyRecorder{status: []int{401, 401}}
	var events []provider.CredentialEvent
	o := poolClient(t, rec, credentialPool(t, "a", "b"), &events)
	resp, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	if err == nil || !errors.Is(err, credpool.ErrExhausted) || !strings.Contains(err.Error(), "credential_exhausted") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaked a value: %v", err)
	}
	if resp.Attempts != 2 || len(rec.seen()) != 2 || len(events) != 1 {
		t.Fatalf("attempts=%d keys=%d events=%d", resp.Attempts, len(rec.seen()), len(events))
	}
	// Every credential cools down: the next call sends nothing instead of
	// looping over doomed keys.
	if _, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil); !errors.Is(err, credpool.ErrExhausted) {
		t.Fatalf("second call: %v", err)
	}
	if got := rec.seen(); len(got) != 2 {
		t.Fatalf("cooldown still sent requests: %v", got)
	}
}

func checkNoRotation(t *testing.T, status, want int) {
	t.Helper()
	rec := &keyRecorder{}
	statuses := []int{status}
	if status == 503 {
		statuses = []int{503, 503, 503}
	}
	rec.status = statuses
	var events []provider.CredentialEvent
	o := poolClient(t, rec, credentialPool(t, "a", "b"), &events)
	resp, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if status == 400 && resp.CredentialID != "a" {
		t.Fatalf("400 did not stay on the first credential: %q", resp.CredentialID)
	}
	if len(events) != 0 {
		t.Fatalf("rotated on %d: events=%+v", status, events)
	}
	seen := rec.seen()
	if len(seen) != want {
		t.Fatalf("requests = %d, want %d", len(seen), want)
	}
	for _, key := range seen {
		if key != "sk-a-secret" {
			t.Fatalf("second credential used on %d: %v", status, seen)
		}
	}
}

func TestCredentialPoolIgnoresNonAuthErrors(t *testing.T) {
	t.Parallel()
	t.Run("400", func(t *testing.T) {
		t.Parallel()
		checkNoRotation(t, 400, 1)
	})
	t.Run("503", func(t *testing.T) {
		t.Parallel()
		checkNoRotation(t, 503, retry.MaxAttempts)
	})
}

func TestCredentialPoolSingleCredential(t *testing.T) {
	t.Parallel()
	rec := &keyRecorder{}
	o := poolClient(t, rec, credentialPool(t, "only"), nil)
	resp, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	if err != nil || resp.CredentialID != "only" || resp.Attempts != 1 {
		t.Fatalf("response %+v, error %v", resp, err)
	}
	if got := rec.seen(); len(got) != 1 || got[0] != "sk-only-secret" {
		t.Fatalf("keys = %v", got)
	}
}

func TestCredentialPoolRejectsSignerAndStaticKey(t *testing.T) {
	t.Parallel()
	pool := credentialPool(t, "a")
	rt := provider.Route{Provider: "fake", Model: "m", BaseURL: "http://127.0.0.1", Auth: auth.Bearer("x")}
	provider.WithCredentialPool(&rt, pool)
	o, err := provider.NewOpenAI(nil, &provider.Config{Routes: []provider.Route{rt}})
	if !errors.Is(err, provider.ErrConfig) || o != nil {
		t.Fatalf("pool + signer: %v", err)
	}
}
