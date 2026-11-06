package photon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/outq"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

type allowPacer struct{}

func (allowPacer) TakeChat(string, string, bool, time.Time) (bool, time.Duration) { return true, 0 }
func (allowPacer) Penalize(string, time.Time)                                     {}

type secrets map[string]string

func (s secrets) Get(_ context.Context, name string) (string, error) {
	value, ok := s[name]
	if !ok {
		return "", errors.New("missing")
	}
	return value, nil
}

type recorder struct {
	mu   sync.Mutex
	in   []gateway.Inbound
	seen chan gateway.Inbound
}

func newRecorder() *recorder { return &recorder{seen: make(chan gateway.Inbound, 8)} }

func (r *recorder) Handle(_ context.Context, in *gateway.Inbound) error {
	r.mu.Lock()
	r.in = append(r.in, *in)
	r.mu.Unlock()
	r.seen <- *in
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.in)
}

func newScheduler(t *testing.T) *outq.Scheduler {
	t.Helper()
	sched, err := outq.New(&outq.Options{Pacer: allowPacer{}, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		if runErr := sched.Run(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Errorf("scheduler: %v", runErr)
		}
	}()
	return sched
}

type fakeSidecar struct {
	srv      *httptest.Server
	mu       sync.Mutex
	requests int
	auth     []string
	sent     []map[string]string
	batch    string
}

func newFakeSidecar(t *testing.T, batch string) *fakeSidecar {
	t.Helper()
	f := &fakeSidecar{batch: batch}
	f.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests++
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/messages") {
			writeJSON(t, rw, f.batch)
			return
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.sent = append(f.sent, payload)
		f.mu.Unlock()
		writeJSON(t, rw, `{"id":"m1"}`)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func writeJSON(t *testing.T, rw http.ResponseWriter, body string) {
	t.Helper()
	if _, err := rw.Write([]byte(body)); err != nil {
		t.Errorf("write: %v", err)
	}
}

func (f *fakeSidecar) snapshot() (int, []string, []map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests, append([]string(nil), f.auth...), append([]map[string]string(nil), f.sent...)
}

func buildAdapter(t *testing.T, cfg *Config, src gwplat.SecretSource, h gwplat.Handler, sched *outq.Scheduler) (*Adapter, error) {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	desc, err := reg.Lookup("photon")
	if err != nil {
		return nil, fmt.Errorf("lookup: %w", err)
	}
	out, err := gwplat.NewOutbox(&desc, sched)
	if err != nil {
		return nil, fmt.Errorf("outbox: %w", err)
	}
	return New(t.Context(), cfg, &desc, src, out, h)
}

// TestF7PLAT014Photon covers PLAT-014: zero-network sidecar probe, long-poll
// dispatch, dedupe and the authenticated send.
func TestF7PLAT014Photon(t *testing.T) {
	sidecar := newFakeSidecar(t, `{"messages":[{"id":"p1","chat":"c1","from":"a1","text":"hello"},{"id":"p1","chat":"c1","from":"a1","text":"hello"},{"id":"p2","chat":"g1","from":"a2","text":"no mention","group":true}]}`)
	t.Run("sidecar_absent", func(t *testing.T) { assertPhotonAbsent(t, sidecar) })
	t.Run("missing_secret", func(t *testing.T) { assertPhotonSecret(t, sidecar) })
	t.Run("poll_and_send", func(t *testing.T) {
		rec := newRecorder()
		cfg := &Config{Enabled: true, BaseURL: sidecar.srv.URL, ProjectID: "proj", HTTPClient: netx.LoopbackClient(), Present: func() error { return nil }}
		ad, err := buildAdapter(t, cfg, secrets{"photon_project_secret": "s"}, rec, newScheduler(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := ad.poll(t.Context()); err != nil {
			t.Fatal(err)
		}
		select {
		case in := <-rec.seen:
			if in.ChatID != "c1" || in.SenderID != "a1" || in.UpdateID != "photon:p1" {
				t.Fatalf("inbound = %+v", in)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no inbound")
		}
		if rec.count() != 1 {
			t.Fatalf("turns = %d, want 1 (replay/mention gates)", rec.count())
		}
		assertPhotonSend(t, ad, sidecar)
	})
}

func assertPhotonAbsent(t *testing.T, sidecar *fakeSidecar) {
	t.Helper()
	cfg := &Config{
		Enabled: true, BaseURL: sidecar.srv.URL, ProjectID: "proj", HTTPClient: netx.LoopbackClient(),
		Present: func() error { return errors.New("socket missing") },
	}
	_, err := buildAdapter(t, cfg, secrets{"photon_project_secret": "s"}, newRecorder(), newScheduler(t))
	if !errors.Is(err, ErrSidecarAbsent) || !errors.Is(err, gwplat.ErrUnavailable) {
		t.Fatalf("absent sidecar = %v, want ErrSidecarAbsent + module_unavailable", err)
	}
	if requests, _, _ := sidecar.snapshot(); requests != 0 {
		t.Fatalf("absent sidecar made %d requests", requests)
	}
}

func assertPhotonSecret(t *testing.T, sidecar *fakeSidecar) {
	t.Helper()
	cfg := &Config{Enabled: true, BaseURL: sidecar.srv.URL, ProjectID: "proj", HTTPClient: netx.LoopbackClient(), Present: func() error { return nil }}
	if _, err := buildAdapter(t, cfg, secrets{}, newRecorder(), newScheduler(t)); !errors.Is(err, gwplat.ErrMissingSecret) {
		t.Fatalf("missing secret = %v", err)
	}
}

func assertPhotonSend(t *testing.T, ad *Adapter, sidecar *fakeSidecar) {
	t.Helper()
	if err := ad.Send(t.Context(), "c1", "hello **world**"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for _, _, sent := sidecar.snapshot(); len(sent) == 0 && time.Now().Before(deadline); _, _, sent = sidecar.snapshot() {
		select {
		case <-t.Context().Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
	_, auth, sent := sidecar.snapshot()
	if len(sent) != 1 || sent[0]["chat"] != "c1" || auth[len(auth)-1] != "Bearer s" {
		t.Fatalf("sent = %v auth = %v", sent, auth)
	}
}
