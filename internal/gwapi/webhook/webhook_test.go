package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

const testSecret = "s3cr3t-delivery-value"

type secretMap map[string]string

func (m secretMap) Get(_ context.Context, name string) (string, error) {
	value, ok := m[name]
	if !ok {
		return "", errors.New("secret not found")
	}
	return value, nil
}

func webhookDescriptor(t *testing.T) gwplat.Descriptor {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	d, err := reg.Lookup("webhook")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

type captureHandler struct{ got []*gateway.Inbound }

func (h *captureHandler) Handle(_ context.Context, in *gateway.Inbound) error {
	h.got = append(h.got, in)
	return nil
}

func newTestAdapter(t *testing.T, cfg *Config, h gwplat.Handler) *Adapter {
	t.Helper()
	desc := webhookDescriptor(t)
	if cfg == nil {
		cfg = &Config{Enabled: true}
	}
	a, err := New(t.Context(), cfg, &desc, secretMap{"webhook_secret": testSecret}, nil, h)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func validEnvelope(id string) Envelope {
	return Envelope{ID: id, Event: "message.created", Chat: "room-1", Sender: "alice", Text: "hello"}
}

func signedBody(t *testing.T, env *Envelope) (http.Header, []byte) {
	t.Helper()
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{}
	header.Set(SignatureHeader, Sign([]byte(testSecret), body))
	return header, body
}

func post(t *testing.T, a *Adapter, header http.Header, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/webhook", bytes.NewReader(body))
	maps.Copy(req.Header, header)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	return rec
}

// TestF7PLAT029Webhook (PLAT-029): the receiver verifies the shared secret,
// dedupes a delivery id, applies the mention policy and never lets an unsigned
// or oversized body reach the handler.
func TestF7PLAT029Webhook(t *testing.T) {
	t.Parallel()
	h := &captureHandler{}
	a := newTestAdapter(t, nil, h)
	if a.Descriptor().Name != "webhook" || a.Outbox() != nil {
		t.Fatalf("descriptor = %+v outbox = %v", a.Descriptor(), a.Outbox())
	}

	env := validEnvelope("d-1")
	header, body := signedBody(t, &env)
	if rec := post(t, a, header, body); rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("first delivery = %d %q", rec.Code, rec.Body.String())
	}
	if len(h.got) != 1 {
		t.Fatalf("dispatched %d, want 1", len(h.got))
	}
	in := h.got[0]
	if in.Platform != "webhook" || in.ChatID != "room-1" || in.SenderID != "alice" ||
		in.UpdateID != "webhook:d-1" || in.Text != "hello" || in.Payload != string(body) {
		t.Fatalf("inbound = %+v", in)
	}

	if rec := post(t, a, header, body); rec.Code != http.StatusOK || len(h.got) != 1 {
		t.Fatalf("replay = %d, dispatched %d", rec.Code, len(h.got))
	}

	checkWebhookRejections(t, a, header, body)
	checkWebhookPolicy(t, a, h)
}

// checkWebhookRejections proves every unsigned, malformed or unsupported
// request is refused before the handler.
func checkWebhookRejections(t *testing.T, a *Adapter, header http.Header, body []byte) {
	t.Helper()
	if rec := post(t, a, http.Header{}, body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned = %d, want 401", rec.Code)
	}
	bad := header.Clone()
	bad.Set(SignatureHeader, "sha256=0000")
	if rec := post(t, a, bad, body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature = %d, want 401", rec.Code)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/webhook", nil)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("GET = %d allow=%q", rec.Code, rec.Header().Get("Allow"))
	}

	cases := map[string]Envelope{
		"bad event": {ID: "d-2", Event: "Bad Event", Chat: "room-1", Sender: "alice"},
		"bad id":    {ID: "d 2", Event: "message.created", Chat: "room-1", Sender: "alice"},
		"bad chat":  {ID: "d-3", Event: "message.created", Chat: "", Sender: "alice"},
	}
	for name := range cases {
		env := cases[name]
		hdr, raw := signedBody(t, &env)
		if rec := post(t, a, hdr, raw); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", name, rec.Code)
		}
	}
}

// checkWebhookPolicy proves the core mention policy applies to webhook groups.
func checkWebhookPolicy(t *testing.T, a *Adapter, h *captureHandler) {
	t.Helper()
	group := validEnvelope("d-4")
	group.Group, group.Mentioned = true, false
	hdr, raw := signedBody(t, &group)
	if rec := post(t, a, hdr, raw); rec.Code != http.StatusOK {
		t.Fatalf("group delivery = %d", rec.Code)
	}
	if len(h.got) != 1 {
		t.Fatalf("group without mention dispatched: %d", len(h.got))
	}
}

// TestF7WebhookSignature: the header is HMAC-SHA256 of the raw body; one
// changed byte, a missing prefix, or a wrong secret is a typed rejection.
func TestF7WebhookSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"id":"d-1"}`)
	got := Sign([]byte("key"), body)
	if want := "sha256=f783d108a23610af7b99e55b58889b53e3328865b63d83eb459042ce7261c2af"; got != want {
		t.Fatalf("signature = %s, want %s", got, want)
	}
	if got != Sign([]byte("key"), body) {
		t.Fatal("Sign is not deterministic")
	}
	if got == Sign([]byte("other"), body) {
		t.Fatal("a different secret produced the same signature")
	}
	header := http.Header{}
	header.Set(SignatureHeader, Sign([]byte(testSecret), body))
	a := newTestAdapter(t, nil, &captureHandler{})
	if err := a.verify(header, body); err != nil {
		t.Fatalf("valid signature refused: %v", err)
	}
	for i := range body {
		tampered := bytes.Clone(body)
		tampered[i] ^= 0xff
		if err := a.verify(header, tampered); !errors.Is(err, ErrSignature) {
			t.Fatalf("byte %d tamper = %v, want ErrSignature", i, err)
		}
	}
	for _, value := range []string{"", got[7:], "sha256=zz", "sha256="} {
		h := http.Header{}
		if value != "" {
			h.Set(SignatureHeader, value)
		}
		if err := a.verify(h, body); !errors.Is(err, ErrSignature) {
			t.Fatalf("header %q = %v, want ErrSignature", value, err)
		}
	}
}

// TestF7WebhookDedupe: the replay window is keyed by the delivery id and
// bounded in time, so a retry after expiry is admitted again.
func TestF7WebhookDedupe(t *testing.T) {
	t.Parallel()
	start := time.Unix(1_700_000_000, 0)
	now := start
	h := &captureHandler{}
	a := newTestAdapter(t, &Config{Enabled: true, Now: func() time.Time { return now }}, h)
	env := validEnvelope("retry-1")
	header, body := signedBody(t, &env)
	if n, err := a.Handle(t.Context(), header, body); err != nil || n != 1 {
		t.Fatalf("first = %d, %v", n, err)
	}
	now = start.Add(DefaultDedupeTTL - time.Second)
	if n, err := a.Handle(t.Context(), header, body); err != nil || n != 0 {
		t.Fatalf("replay inside TTL = %d, %v", n, err)
	}
	now = start.Add(DefaultDedupeTTL + time.Second)
	if n, err := a.Handle(t.Context(), header, body); err != nil || n != 1 {
		t.Fatalf("replay after TTL = %d, %v", n, err)
	}
	if len(h.got) != 2 {
		t.Fatalf("dispatched %d, want 2", len(h.got))
	}
}

// TestF7WebhookConfig: zero config is disabled, a missing secret fails closed,
// and the body limit is applied before any parse.
func TestF7WebhookConfig(t *testing.T) {
	t.Parallel()
	desc := webhookDescriptor(t)
	disabled := &Config{}
	if _, err := New(t.Context(), disabled, &desc, secretMap{"webhook_secret": testSecret}, nil, &captureHandler{}); !errors.Is(err, gwplat.ErrUnavailable) {
		t.Fatalf("disabled = %v, want ErrUnavailable", err)
	}
	for name, tc := range map[string]struct {
		desc   *gwplat.Descriptor
		cfg    *Config
		secret gwplat.SecretSource
		h      gwplat.Handler
	}{
		"nil descriptor": {nil, &Config{Enabled: true}, secretMap{"webhook_secret": testSecret}, &captureHandler{}},
		"nil config":     {&desc, nil, secretMap{"webhook_secret": testSecret}, &captureHandler{}},
		"missing secret": {&desc, &Config{Enabled: true}, secretMap{}, &captureHandler{}},
		"nil handler":    {&desc, &Config{Enabled: true}, secretMap{"webhook_secret": testSecret}, nil},
	} {
		if _, err := New(t.Context(), tc.cfg, tc.desc, tc.secret, nil, tc.h); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}

	a := newTestAdapter(t, &Config{Enabled: true, MaxBodyBytes: 64}, &captureHandler{})
	big := []byte(strings.Repeat("x", 65))
	if rec := post(t, a, http.Header{}, big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized = %d, want 413", rec.Code)
	}
	if _, err := a.Admit(http.Header{}, big); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Admit oversized = %v, want ErrTooLarge", err)
	}
}
