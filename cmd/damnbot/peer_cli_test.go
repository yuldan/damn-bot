//go:build !minimal

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/gwpeer"
)

func runPeerCLI(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := runPeer(args, &out, &errb)
	return code, out.String(), errb.String()
}

func publicKeyOf(t *testing.T, key ed25519.PrivateKey) ed25519.PublicKey {
	t.Helper()
	out, ok := key.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("private key did not yield an Ed25519 public key")
	}
	return out
}

func TestF7CLI027Peer(t *testing.T) {
	dir := t.TempDir()
	peerKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	fake := fakePeerServer(t)
	testPeerRegistry(t, dir, fake, peerKey)
}

// fakePeerServer accepts one DM addressed to peer b.
func fakePeerServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/peer/v1/dm" {
			http.NotFound(w, r)
			return
		}
		var env gwpeer.Envelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		if env.To != "b" {
			t.Errorf("dm routed to %q, want b", env.To)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"ok": true}); err != nil {
			t.Errorf("reply: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("B_TOKEN", gwpeer.NewID())
	return srv
}

func testPeerRegistry(t *testing.T, dir string, fake *httptest.Server, peerKey ed25519.PrivateKey) {
	t.Helper()
	pub := base64.StdEncoding.EncodeToString(publicKeyOf(t, peerKey))
	code, _, errOut := runPeerCLI("add", "--data-dir", dir, "--id", "b", "--url", fake.URL,
		"--token-ref", "B_TOKEN", "--pubkey", pub)
	if code != 0 {
		t.Fatalf("peer add = %d: %s", code, errOut)
	}
	code, out, _ := runPeerCLI("list", "--data-dir", dir)
	if code != 0 || !strings.Contains(out, "b\t"+fake.URL) {
		t.Fatalf("peer list = %d %q", code, out)
	}
	code, _, errOut = runPeerCLI("dm", "--data-dir", dir, "--id", "a", "--peer", "foreign", "--text", "x")
	if code == 0 || !strings.Contains(errOut, "not registered") {
		t.Fatalf("foreign dm = %d %q, want rejection", code, errOut)
	}
	code, out, errOut = runPeerCLI("dm", "--data-dir", dir, "--id", "a", "--peer", "b", "--text", "hello")
	if code != 0 || !strings.Contains(out, "sent ") {
		t.Fatalf("peer dm = %d %q %q", code, out, errOut)
	}
	if rc, _, rmErr := runPeerCLI("remove", "--data-dir", dir, "b"); rc != 0 {
		t.Fatalf("peer remove = %d: %s", rc, rmErr)
	}
	code, out, _ = runPeerCLI("list", "--data-dir", dir)
	if code != 0 || strings.TrimSpace(out) != "" {
		t.Fatalf("peer list after remove = %d %q", code, out)
	}
}
