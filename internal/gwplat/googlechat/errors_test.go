package googlechat

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

// gcDesc returns the registered descriptor of the adapter.
func gcDesc(t *testing.T) gwplat.Descriptor {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup("googlechat")
	if err != nil {
		t.Fatal(err)
	}
	return desc
}

// gcAdapter builds a valid adapter with a signed-token seam.
func gcAdapter(t *testing.T, cfg *Config, h gwplat.Handler) (*Adapter, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	base := &Config{
		Enabled: true, JWTKeys: map[string]*rsa.PublicKey{"kid": &key.PublicKey}, JWTAudience: "project-1",
		Token: func(context.Context) (string, error) { return "oauth", nil },
	}
	if cfg != nil {
		base.API = cfg.API
		base.Token = cfg.Token
	}
	if h == nil {
		h = gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil })
	}
	desc := gcDesc(t)
	a, err := New(t.Context(), base, &desc, secrets{"googlechat_service_account": "x"}, newOutbox(t, "googlechat"), h)
	if err != nil {
		t.Fatal(err)
	}
	return a, key
}

// TestGoogleChatNewRejectsInvalidActivation covers every fail-closed branch.
func TestGoogleChatNewRejectsInvalidActivation(t *testing.T) {
	desc := gcDesc(t)
	other := desc
	other.Name = "slack"
	out := newOutbox(t, "googlechat")
	h := gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil })
	okCfg := func() *Config { return &Config{Enabled: true} }
	okSecrets := secrets{"googlechat_service_account": "x"}
	cases := []struct {
		name string
		cfg  *Config
		d    *gwplat.Descriptor
		o    *gwplat.Outbox
		h    gwplat.Handler
		src  gwplat.SecretSource
		want error
	}{
		{"nil config", nil, &desc, out, h, okSecrets, ErrConfig},
		{"nil descriptor", okCfg(), nil, out, h, okSecrets, ErrConfig},
		{"foreign descriptor", okCfg(), &other, out, h, okSecrets, ErrConfig},
		{"disabled", &Config{}, &desc, out, h, okSecrets, gwplat.ErrUnavailable},
		{"nil outbox", okCfg(), &desc, nil, h, okSecrets, ErrConfig},
		{"nil handler", okCfg(), &desc, out, nil, okSecrets, ErrConfig},
		{"missing secret", okCfg(), &desc, out, h, secrets{}, gwplat.ErrMissingSecret},
	}
	for _, tc := range cases {
		if _, err := New(t.Context(), tc.cfg, tc.d, tc.src, tc.o, tc.h); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// TestGoogleChatSendErrors: without a token source nothing is posted, an
// upstream token failure keeps ErrNoToken, and the outbox refuses a reply with
// no space before any network call.
func TestGoogleChatSendErrors(t *testing.T) {
	a, _ := gcAdapter(t, &Config{Token: nil}, nil)
	if err := a.Send(t.Context(), "spaces/AAA", "hi"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("nil token source = %v, want ErrNoToken", err)
	}
	failing := func(context.Context) (string, error) { return "", errors.New("token down") }
	a, _ = gcAdapter(t, &Config{Token: failing}, nil)
	if err := a.Send(t.Context(), "spaces/AAA", "hi"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("token failure = %v, want ErrNoToken", err)
	}
	a, _ = gcAdapter(t, nil, nil)
	if err := a.Send(t.Context(), "", "hi"); !errors.Is(err, gwplat.ErrNoReply) {
		t.Fatalf("empty space = %v, want ErrNoReply", err)
	}
}

// TestGoogleChatHandlerGuards: the callback is fail-closed for wrong verbs,
// missing/invalid JWTs, oversized and corrupt bodies, and reports a handler
// failure so Chat retries.
func TestGoogleChatHandlerGuards(t *testing.T) {
	a, key := gcAdapter(t, nil, nil)
	token := sign(t, key, "kid", map[string]any{"iss": "chat@system.gserviceaccount.com", "aud": "project-1", "exp": time.Now().Add(time.Minute).Unix()})
	cases := []struct {
		name   string
		method string
		body   string
		auth   string
		want   int
	}{
		{"wrong verb", http.MethodPut, payload(), "Bearer " + token, http.StatusMethodNotAllowed},
		{"missing jwt", http.MethodPost, payload(), "", http.StatusUnauthorized},
		{"corrupt jwt", http.MethodPost, payload(), "Bearer a.b.c", http.StatusUnauthorized},
		{"oversized", http.MethodPost, strings.Repeat("x", (1<<20)+1), "Bearer " + token, http.StatusRequestEntityTooLarge},
		{"corrupt json", http.MethodPost, `not-json`, "Bearer " + token, http.StatusBadRequest},
		{"wrong event type", http.MethodPost, `{"type":"ADDED_TO_SPACE"}`, "Bearer " + token, http.StatusBadRequest},
	}
	for _, tc := range cases {
		req := httptest.NewRequestWithContext(t.Context(), tc.method, "/googlechat", strings.NewReader(tc.body))
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
	failing, fkey := gcAdapter(t, nil, gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error {
		return errors.New("boom")
	}))
	ftoken := sign(t, fkey, "kid", map[string]any{"iss": "chat@system.gserviceaccount.com", "aud": "project-1", "exp": time.Now().Add(time.Minute).Unix()})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/googlechat", strings.NewReader(payload()))
	req.Header.Set("Authorization", "Bearer "+ftoken)
	rec := httptest.NewRecorder()
	failing.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("dispatch failure = %d, want 500", rec.Code)
	}
}

// TestGoogleChatParse covers the mapping and its rejection cases.
func TestGoogleChatParse(t *testing.T) {
	for _, raw := range []string{
		`{`,
		`{}`,
		`{"type":"ADDED_TO_SPACE","message":{"name":"spaces/A/messages/M"},"space":{"name":"spaces/A"}}`,
		`{"type":"MESSAGE","message":{"text":"x"},"space":{"name":"spaces/A"}}`,
		`{"type":"MESSAGE","message":{"name":"spaces/A/messages/M"},"space":{"name":""}}`,
	} {
		if _, ok := parse([]byte(raw)); ok {
			t.Errorf("parse(%q) accepted", raw)
		}
	}
	in, ok := parse([]byte(`{"type":"MESSAGE","message":{"name":"spaces/A/messages/M","text":"hi @bot"},"space":{"name":"spaces/A","type":"ROOM"},"user":{"name":"users/1"}}`))
	if !ok || !in.Group || !in.Mentioned || in.SenderID != "users/1" || in.ChatID != "spaces/A" || in.UpdateID != "spaces/A/messages/M" {
		t.Fatalf("room parse = %+v ok=%v", in, ok)
	}
	in, ok = parse([]byte(`{"type":"MESSAGE","message":{"name":"spaces/A/messages/M","text":"hi","sender":{"name":"users/2"}},"space":{"name":"spaces/A","type":"DM"},"user":{"name":"users/1"}}`))
	if !ok || in.Group || !in.Mentioned || in.SenderID != "users/2" {
		t.Fatalf("dm parse = %+v ok=%v", in, ok)
	}
}
