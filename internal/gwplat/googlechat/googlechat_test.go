package googlechat

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

func sign(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	h, herr := json.Marshal(map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"})
	if herr != nil {
		t.Fatal(herr)
	}
	p, perr := json.Marshal(claims)
	if perr != nil {
		t.Fatal(perr)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	digest := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func payload() string {
	return `{"type":"MESSAGE","message":{"name":"spaces/AAA/messages/BBB","text":"halo","sender":{"name":"users/123"}},"space":{"name":"spaces/AAA","type":"DM"},"user":{"name":"users/123"}}`
}

func TestF7PLAT008GoogleChat(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]*rsa.PublicKey{"kid": &key.PublicKey}
	sent := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if derr := json.NewDecoder(r.Body).Decode(&body); derr != nil {
			return
		}
		sent <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	reg, rerr := gwplat.NewDefaultRegistry()
	if rerr != nil {
		t.Fatal(rerr)
	}
	desc, derr := reg.Lookup("googlechat")
	if derr != nil {
		t.Fatal(derr)
	}
	got := 0
	a, err := New(t.Context(), &Config{
		Enabled: true, API: srv.URL, JWTKeys: keys, JWTAudience: "project-1",
		Token: func(context.Context) (string, error) { return "oauth", nil },
	}, &desc,
		secrets{"googlechat_service_account": "x"}, newOutbox(t, "googlechat"),
		gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { got++; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	token := sign(t, key, "kid", map[string]any{"iss": "chat@system.gserviceaccount.com", "aud": "project-1", "exp": time.Now().Add(time.Minute).Unix()})
	for range 2 {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/googlechat", strings.NewReader(payload()))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("valid = %d", rec.Code)
		}
	}
	if got != 1 {
		t.Fatalf("turns = %d", got)
	}
	if err := a.Send(t.Context(), "spaces/AAA", "**halo**"); err != nil {
		t.Fatal(err)
	}
	body := awaitValue(t, sent)
	if !strings.Contains(string(mustJSON(t, body)), "halo") {
		t.Fatalf("sent = %v", body)
	}
}

func TestF7GoogleChatVerify(t *testing.T) {
	key, kerr := rsa.GenerateKey(rand.Reader, 2048)
	if kerr != nil {
		t.Fatal(kerr)
	}
	keys := map[string]*rsa.PublicKey{"kid": &key.PublicKey}
	now := time.Unix(1_800_000_000, 0)
	base := func() map[string]any {
		return map[string]any{"iss": "chat@system.gserviceaccount.com", "aud": "project-1", "exp": now.Add(time.Minute).Unix()}
	}
	if _, err := gwplat.VerifyJWT(sign(t, key, "kid", base()), keys, "chat@system.gserviceaccount.com", "project-1", now); err != nil {
		t.Fatal(err)
	}
	other, oerr := rsa.GenerateKey(rand.Reader, 2048)
	if oerr != nil {
		t.Fatal(oerr)
	}
	cases := []string{
		sign(t, other, "kid", base()),
		sign(t, key, "other", base()),
		sign(t, key, "kid", map[string]any{"iss": "chat@system.gserviceaccount.com", "aud": "other", "exp": now.Add(time.Minute).Unix()}),
		sign(t, key, "kid", map[string]any{"iss": "evil", "aud": "project-1", "exp": now.Add(time.Minute).Unix()}),
		sign(t, key, "kid", map[string]any{"iss": "chat@system.gserviceaccount.com", "aud": "project-1", "exp": now.Add(-time.Second).Unix()}),
		"a.b.c",
	}
	for i, token := range cases {
		if _, err := gwplat.VerifyJWT(token, keys, "chat@system.gserviceaccount.com", "project-1", now); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
