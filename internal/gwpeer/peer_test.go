package gwpeer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type tokenMap map[string]string

func (m tokenMap) Token(_ context.Context, ref string) (string, error) {
	if v, ok := m[ref]; ok && v != "" {
		return v, nil
	}
	return "", ErrTokenMissing
}

type dispatcher struct {
	mu    sync.Mutex
	count int
	last  *Envelope
}

func (d *dispatcher) dispatch(_ context.Context, env *Envelope) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.count++
	d.last = env
	return nil
}

func (d *dispatcher) total() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.count
}

func testKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func pub(t *testing.T, key ed25519.PrivateKey) ed25519.PublicKey {
	t.Helper()
	out, ok := key.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("private key did not yield an Ed25519 public key")
	}
	return out
}

func testServer(t *testing.T, id string, peers map[string]Peer, tokens tokenMap, d *dispatcher) *httptest.Server {
	t.Helper()
	srv := &Server{
		ID: id,
		Peers: func(peer string) (Peer, bool) {
			p, ok := peers[peer]
			return p, ok
		},
		Tokens:   tokens,
		Guard:    NewReplayGuard(nil, time.Minute),
		Dispatch: d.dispatch,
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestF7GW013PeerDM(t *testing.T) {
	t.Parallel()
	aKey, bKey := testKey(t), testKey(t)
	secret := NewID()
	tokens := tokenMap{"A_TOKEN": secret, "B_TOKEN": secret}
	peersA := map[string]Peer{"a": {
		ID: "a", URL: "https://a.invalid", TokenRef: "A_TOKEN", PublicKey: pub(t, aKey),
	}}
	d := &dispatcher{}
	b := testServer(t, "b", peersA, tokens, d)

	a := &Client{LocalID: "a", Key: aKey, Tokens: tokens, HTTP: http.DefaultClient, Now: time.Now}
	env, err := a.Send(t.Context(), &Peer{
		ID: "b", URL: b.URL, TokenRef: "B_TOKEN", PublicKey: pub(t, bKey),
	}, "dm-1", "hello peer")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if d.total() != 1 {
		t.Fatalf("dispatch count = %d, want 1", d.total())
	}
	if d.last.ID != env.ID || d.last.Text != "hello peer" || d.last.From != "a" {
		t.Fatalf("dispatched envelope = %+v", d.last)
	}
	if verr := d.last.Verify(pub(t, aKey)); verr != nil {
		t.Fatalf("dispatched signature: %v", verr)
	}
	bad := &Client{LocalID: "a", Key: aKey, Tokens: tokenMap{"B_TOKEN": "wrong"}, HTTP: http.DefaultClient}
	if _, berr := bad.Send(t.Context(), &Peer{
		ID: "b", URL: b.URL, TokenRef: "B_TOKEN", PublicKey: pub(t, bKey),
	}, "dm-2", "must not arrive"); !errors.Is(berr, ErrUnauthorized) {
		t.Fatalf("wrong token error = %v, want ErrUnauthorized", berr)
	}
	if d.total() != 1 {
		t.Fatalf("dispatch after unauthorized = %d, want 1", d.total())
	}
}

func TestF7PeerReplay(t *testing.T) {
	t.Parallel()
	aKey := testKey(t)
	secret := NewID()
	tokens := tokenMap{"A_TOKEN": secret, "B_TOKEN": secret}
	peers := map[string]Peer{"a": {
		ID: "a", URL: "https://a.invalid", TokenRef: "A_TOKEN", PublicKey: pub(t, aKey),
	}}
	d := &dispatcher{}
	b := testServer(t, "b", peers, tokens, d)

	env := &Envelope{
		Version: ProtocolVersion, ID: NewID(), From: "a", To: "b", Nonce: NewID(),
		Timestamp: time.Now().Unix(), Chat: "dm-1", Text: "once",
	}
	if serr := env.Sign(aKey); serr != nil {
		t.Fatal(serr)
	}
	body, merr := json.Marshal(env)
	if merr != nil {
		t.Fatal(merr)
	}
	post := func() int {
		req, rerr := http.NewRequestWithContext(t.Context(), http.MethodPost, b.URL+"/peer/v1/dm", bytes.NewReader(body))
		if rerr != nil {
			t.Fatal(rerr)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set(peerAuthHeader, "a")
		resp, derr := b.Client().Do(req)
		if derr != nil {
			t.Fatal(derr)
		}
		if cerr := resp.Body.Close(); cerr != nil {
			t.Fatal(cerr)
		}
		return resp.StatusCode
	}
	if code := post(); code != http.StatusOK {
		t.Fatalf("first post = %d, want 200", code)
	}
	if code := post(); code != http.StatusConflict {
		t.Fatalf("replay post = %d, want 409", code)
	}
	if d.total() != 1 {
		t.Fatalf("dispatch count = %d, want exactly 1", d.total())
	}
	forged := env
	forged.Signature = append([]byte(nil), env.Signature...)
	forged.Signature[0] ^= 0xff
	body, merr = json.Marshal(&forged)
	if merr != nil {
		t.Fatal(merr)
	}
	if code := post(); code != http.StatusBadRequest {
		t.Fatalf("forged post = %d, want 400", code)
	}
	if d.total() != 1 {
		t.Fatalf("dispatch after forged = %d, want 1", d.total())
	}
}

func TestEnvelopeRejectsStaleAndMalformed(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	good := Envelope{
		Version: ProtocolVersion, ID: "m", From: "a", To: "b", Nonce: "n",
		Timestamp: now.Unix(), Chat: "c", Text: "x",
	}
	if err := good.Validate(now, time.Minute); err != nil {
		t.Fatalf("valid envelope: %v", err)
	}
	stale := good
	stale.Timestamp = now.Add(-2 * time.Minute).Unix()
	if err := stale.Validate(now, time.Minute); !errors.Is(err, ErrExpired) {
		t.Fatalf("stale envelope = %v, want ErrExpired", err)
	}
	bad := good
	bad.Version = 9
	if err := bad.Validate(now, time.Minute); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("bad version = %v, want ErrInvalidEnvelope", err)
	}
}

// TestEnvTokens keeps the transport token out of the config: only a real
// environment variable name resolves, and a missing value is an error rather
// than an empty token.
func TestEnvTokens(t *testing.T) {
	t.Setenv("PEER_TEST_TOKEN", "s3cret")
	got, err := EnvTokens{}.Token(t.Context(), "PEER_TEST_TOKEN")
	if err != nil || got != "s3cret" {
		t.Fatalf("Token = %q, %v", got, err)
	}
	if _, err := (EnvTokens{}).Token(t.Context(), "PEER_TEST_MISSING"); !errors.Is(err, ErrTokenMissing) {
		t.Fatalf("missing token = %v, want ErrTokenMissing", err)
	}
	for _, ref := range []string{"", "not a name", "1BAD", "bad-name"} {
		if _, err := (EnvTokens{}).Token(t.Context(), ref); !errors.Is(err, ErrTokenMissing) {
			t.Errorf("Token(%q) = %v, want ErrTokenMissing", ref, err)
		}
	}
}

// TestPeerValidateURL keeps the registry entry fail-closed: HTTPS anywhere,
// plaintext HTTP only on loopback, and never credentials in the URL.
func TestPeerValidateURL(t *testing.T) {
	t.Parallel()
	key := pub(t, testKey(t))
	base := Peer{ID: "b", TokenRef: "B_TOKEN", PublicKey: key}
	for _, tc := range []struct {
		name string
		url  string
		ok   bool
	}{
		{"https", "https://peer.example:8443", true},
		{"loopback-http", "http://127.0.0.1:8788", true},
		{"localhost-http", "http://localhost:8788", true},
		{"public-http", "http://peer.example", false},
		{"credentials", "https://user:pass@peer.example", false},
		{"scheme", "ftp://peer.example", false},
	} {
		p := base
		p.URL = tc.url
		err := p.Validate()
		if tc.ok && err != nil {
			t.Errorf("%s: Validate(%q) = %v, want ok", tc.name, tc.url, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalidPeer) {
			t.Errorf("%s: Validate(%q) = %v, want ErrInvalidPeer", tc.name, tc.url, err)
		}
	}
}
