package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

// googleFixture is a generated service-account key plus a fake token server
// that verifies the RS256 assertion it receives.
type googleFixture struct {
	key     *rsa.PrivateKey
	file    string
	server  *httptest.Server
	mu      sync.Mutex
	calls   int
	lastJWT string
	fail    bool
}

func newGoogleFixture(t *testing.T) *googleFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &googleFixture{key: key}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if perr := r.ParseForm(); perr != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.calls++
		f.lastJWT = r.Form.Get("assertion")
		fail := f.fail
		f.mu.Unlock()
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || !f.verify(f.lastJWT) {
			http.Error(w, "bad assertion", http.StatusUnauthorized)
			return
		}
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"google-token-SECRET","token_type":"Bearer","expires_in":3600}`)) //nolint:errcheck // test response
	}))
	t.Cleanup(f.server.Close)
	block, merr := x509.MarshalPKCS8PrivateKey(key)
	if merr != nil {
		t.Fatal(merr)
	}
	creds, err := json.Marshal(map[string]string{
		"type": "service_account", "client_email": "bot@example.iam.gserviceaccount.com",
		"private_key_id": "kid-1", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: block})),
		"token_uri": f.server.URL + "/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.file = filepath.Join(t.TempDir(), "adc.json")
	if err := os.WriteFile(f.file, creds, 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *googleFixture) verify(jwt string) bool {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return false
	}
	unsigned := parts[0] + "." + parts[1]
	sig, serr := base64.RawURLEncoding.DecodeString(parts[2])
	if serr != nil {
		return false
	}
	sum := sha256.Sum256([]byte(unsigned))
	if err := rsa.VerifyPKCS1v15(&f.key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		return false
	}
	raw, berr := base64.RawURLEncoding.DecodeString(parts[1])
	if berr != nil {
		return false
	}
	var claims struct {
		Iss   string `json:"iss"`
		Aud   string `json:"aud"`
		Scope string `json:"scope"`
		Iat   int64  `json:"iat"`
		Exp   int64  `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return false
	}
	return claims.Iss == "bot@example.iam.gserviceaccount.com" && claims.Aud == f.server.URL+"/token" &&
		claims.Scope == "https://www.googleapis.com/auth/cloud-platform" && claims.Exp-claims.Iat == 3600
}

func (f *googleFixture) jwt() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastJWT
}

func (f *googleFixture) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func testClient() *netx.Client { return netx.New(netx.Config{}).Client("auth", netx.Inference) }

// TestGoogleJWTCache verifies the RS256 assertion, the ADC env lookup and the
// expiry cache: two signs inside the hour use one token request.
func TestGoogleJWTCache(t *testing.T) {
	f := newGoogleFixture(t)
	now := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	env := func(k string) string {
		if k == GoogleEnvFile {
			return f.file
		}
		return ""
	}
	g, err := NewGoogle(GoogleConfig{
		LookupEnv: env, Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"},
		Client: testClient(), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		req, rerr := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://aiplatform.googleapis.com/v1/x", nil)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if serr := g.Sign(context.Background(), req, nil); serr != nil {
			t.Fatal(serr)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer google-token-SECRET" {
			t.Fatalf("Authorization = %q", got)
		}
	}
	if f.count() != 1 {
		t.Fatalf("token requests = %d, want 1 (cache)", f.count())
	}
	now = now.Add(3600 * time.Second)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://aiplatform.googleapis.com/v1/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Sign(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if f.count() != 2 {
		t.Fatalf("token requests = %d, want 2 after expiry", f.count())
	}
}

// TestGoogleNegative covers the negative controls: no file, a non-service
// account, a plaintext remote endpoint and an error body that must not leak
// the assertion.
func TestGoogleNegative(t *testing.T) {
	f := newGoogleFixture(t)
	if _, err := NewGoogle(GoogleConfig{Client: testClient()}); !errors.Is(err, ErrCredential) {
		t.Fatalf("missing ADC err = %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"type":"authorized_user"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewGoogle(GoogleConfig{CredentialsFile: bad, Client: testClient()}); !errors.Is(err, ErrCredential) {
		t.Fatalf("authorized_user err = %v", err)
	}
	plain := filepath.Join(t.TempDir(), "plain.json")
	if err := os.WriteFile(plain, []byte(`{"type":"service_account","client_email":"a@b.c","private_key":"x","token_uri":"http://example.com/token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewGoogle(GoogleConfig{CredentialsFile: plain, Client: testClient()}); !errors.Is(err, ErrEndpoint) {
		t.Fatalf("plaintext endpoint err = %v", err)
	}
	f.mu.Lock()
	f.fail = true
	f.mu.Unlock()
	g, err := NewGoogle(GoogleConfig{CredentialsFile: f.file, Client: testClient()})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://aiplatform.googleapis.com/v1/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	err = g.Sign(context.Background(), req, nil)
	if err == nil {
		t.Fatal("expected token endpoint failure")
	}
	if strings.Contains(err.Error(), f.jwt()) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error leaked a credential: %v", err)
	}
}
