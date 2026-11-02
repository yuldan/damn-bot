package catalog

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGAPF5_SEC20FetchAndSerial covers the live path: only a 2xx signed body
// is accepted, and the persisted serial rejects a replay/rollback across
// calls (the CLI uses the same FileSeqStore).
func TestGAPF5_SEC20FetchAndSerial(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m := testManifest(KindMCP, 5)
	raw := signedManifest(t, priv, &m)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		if _, werr := w.Write(raw); werr != nil {
			return
		}
	}))
	t.Cleanup(srv.Close)
	client := srv.Client()
	dir := t.TempDir()
	store := FileSeqStore{Dir: dir}
	got, err := Fetch(context.Background(), client, srv.URL, pub, 0)
	if err != nil || got.Serial != 5 {
		t.Fatalf("Fetch = %+v, %v", got, err)
	}
	if err := store.Save(got.Kind, got.Name, got.Serial); err != nil {
		t.Fatal(err)
	}
	if last, lerr := store.Last(got.Kind, got.Name); lerr != nil || last != 5 {
		t.Fatalf("Last = %d, %v", last, lerr)
	}
	if _, err := store.Last(got.Kind, got.Name); err != nil {
		t.Fatal(err)
	}
	// A second fetch of the same serial is a rollback for the next run.
	if _, err := Fetch(context.Background(), client, srv.URL, pub, 5); err == nil || !strings.Contains(err.Error(), "serial") {
		t.Fatalf("replay = %v, want rollback", err)
	}
	if _, err := Fetch(context.Background(), client, srv.URL+"/missing", pub, 0); err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("404 = %v", err)
	}
}

func TestGAPF5_SEC20FileSeqCorruptFailsClosed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := FileSeqStore{Dir: dir}
	path := filepath.Join(dir, "mcp-demo.seq")
	if err := os.WriteFile(path, []byte("not-a-number\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Last(KindMCP, "demo"); err == nil {
		t.Fatal("corrupt serial accepted")
	}
	if err := store.Save(KindMCP, "demo", 7); err != nil {
		t.Fatal(err)
	}
	if last, lerr := store.Last(KindMCP, "demo"); lerr != nil || last != 7 {
		t.Fatalf("serial = %d, %v", last, lerr)
	}
}

// TestGAPF5_SEC20FetchTamper ensures Fetch does not trust the body before the
// signature check.
func TestGAPF5_SEC20FetchTamper(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m := testManifest(KindProvider, 2)
	raw := signedManifest(t, priv, &m)
	var parsed Manifest
	if uerr := json.Unmarshal(raw, &parsed); uerr != nil {
		t.Fatal(uerr)
	}
	parsed.Name = "evil"
	tampered, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, werr := w.Write(tampered); werr != nil {
			return
		}
	}))
	t.Cleanup(srv.Close)
	client := srv.Client()
	if _, err := Fetch(context.Background(), client, srv.URL, pub, 0); err == nil {
		t.Fatal("tampered manifest accepted")
	}
}
