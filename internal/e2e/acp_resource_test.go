package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/secret"
)

// acpDataDir mirrors config.Paths("").DataDir for the harness HOME.
func acpDataDir(home string) string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "AppData", "Local", "damnbot")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "damnbot")
	default:
		return filepath.Join(home, ".local", "share", "damnbot")
	}
}

func openACPStore(t *testing.T, e *Env) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: acpDataDir(e.home), Kind: "cli", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close acp db: %v", cerr)
		}
	})
	return db
}

// TestACPResourceSpillAndMetaPersistence drives the real binary: a >4.000
// char resource is spilled to ctxdb with the correct blob_sha, the cloned
// bytes decrypt/read back, and the raw resource `_meta` survives the session
// reload. An offline provider fails the turn after user_message persistence.
func TestACPResourceSpillAndMetaPersistence(t *testing.T) {
	e := newEnv(t)
	p := startACP(t, e, "[provider]\nmodel = \"openai/gpt-fake\"\n\n[network]\noffline = true\n\n[approval]\npreview_timeout_s = 1\n")
	p.initSession(e.Workdir)
	large := strings.Repeat("resource-body-", 400)
	blobBody := "binary-body-canary"
	blob := base64.StdEncoding.EncodeToString([]byte(blobBody))
	p.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{
		"sessionId": p.sid,
		"prompt": []any{
			map[string]any{"type": "resource", "_meta": map[string]any{"trace": "t-1", "future": map[string]any{"x": 1}}, "resource": map[string]any{"uri": "file:///large.txt", "text": large}},
			map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///blob.bin", "mimeType": "application/octet-stream", "blob": blob}},
		},
	}})
	var f map[string]any
	for f = p.recv(); f["id"] != float64(3); f = p.recv() {
	}
	if f["error"] != nil {
		if code, _ := acpError(t, f); code == -32602 {
			t.Fatalf("valid resource rejected: %v", f)
		}
	}
	db := openACPStore(t, e)
	assertSpilledMessage(t, db, p.sid, large, blobBody)
}

func assertSpilledMessage(t *testing.T, db *ctxdb.Store, sid, large, blobBody string) {
	t.Helper()
	evs, err := db.History(context.Background(), sid, 0, ctxdb.WithHistoryExcluded())
	if err != nil {
		t.Fatal(err)
	}
	var um ctxdb.Event
	for i := range evs {
		if evs[i].Kind == "user_message" {
			um = evs[i]
		}
	}
	if um.Kind != "user_message" || len(um.Attach) != 2 {
		t.Fatalf("user_message = %+v", um)
	}
	if got, rerr := db.ReadBlob(context.Background(), um.Attach[0]); rerr != nil || string(got) != large {
		t.Fatalf("large blob = %d bytes, %v", len(got), rerr)
	}
	if got, rerr := db.ReadBlob(context.Background(), um.Attach[1]); rerr != nil || string(got) != blobBody {
		t.Fatalf("binary blob = %q, %v", got, rerr)
	}
	var payload struct {
		Meta string `json:"meta"`
	}
	if err := json.Unmarshal([]byte(um.Payload), &payload); err != nil {
		t.Fatalf("payload = %s: %v", um.Payload, err)
	}
	var metas []map[string]any
	if err := json.Unmarshal([]byte(payload.Meta), &metas); err != nil || len(metas) != 1 || metas[0]["trace"] != "t-1" {
		t.Fatalf("meta = %q: %v", payload.Meta, err)
	}
}

// TestACPResourceSpillEncryptedAtRest is the negative control: with
// storage.encrypt the blob file never holds plaintext, while the same blob
// reads back through the at-rest codec.
func TestACPResourceSpillEncryptedAtRest(t *testing.T) {
	requireUsableKeyStore(t)
	e := newEnv(t)
	const canary = "PLAINTEXT-CANARY-ACP"
	p := startACP(t, e, "[provider]\nmodel = \"openai/gpt-fake\"\n\n[network]\noffline = true\n\n[storage]\nencrypt = true\n\n[approval]\npreview_timeout_s = 1\n")
	p.initSession(e.Workdir)
	p.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{
		"sessionId": p.sid,
		"prompt":    []any{map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///secret.txt", "text": strings.Repeat(canary, 300)}}},
	}})
	var f map[string]any
	for f = p.recv(); f["id"] != float64(3); f = p.recv() {
	}
	if f["error"] != nil {
		if code, _ := acpError(t, f); code == -32602 {
			t.Fatalf("valid resource rejected: %v", f)
		}
	}
	if err := os.MkdirAll(filepath.Join(e.Workdir, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	assertNoCanary(t, acpDataDir(e.home), canary)
	st, err := secret.New(secret.Options{ConfigDir: appConfigDir(e.home), BackendPresent: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	codec, err := secret.OpenCodec(context.Background(), acpDataDir(e.home), st, false)
	if err != nil {
		t.Fatal(err)
	}
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: acpDataDir(e.home), Kind: "cli", ReadOnly: true, Codec: codec})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close encrypted db: %v", cerr)
		}
	}()
	evs, err := db.History(context.Background(), p.sid, 0, ctxdb.WithHistoryExcluded())
	if err != nil {
		t.Fatal(err)
	}
	for i := range evs {
		if evs[i].Kind != "user_message" || len(evs[i].Attach) != 1 {
			continue
		}
		got, rerr := db.ReadBlob(context.Background(), evs[i].Attach[0])
		if rerr != nil || string(got) != strings.Repeat(canary, 300) {
			t.Fatalf("decrypted blob = %d bytes, %v", len(got), rerr)
		}
		return
	}
	t.Fatal("encrypted user_message with attachment not found")
}

// TestACPResourceBlobTooLarge: a blob above the 8 MiB spill cap is rejected
// as -32602 too_large before any store access.
func TestACPResourceBlobTooLarge(t *testing.T) {
	e := newEnv(t)
	p := startACP(t, e, "[approval]\npreview_timeout_s = 1\n")
	p.initSession(e.Workdir)
	const maxBlob = 8 << 20
	blob := strings.Repeat("A", base64.StdEncoding.EncodedLen(maxBlob)+1)
	p.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{
		"sessionId": p.sid,
		"prompt":    []any{map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///big.bin", "blob": blob}}},
	}})
	code, errorCode := acpError(t, p.recv())
	if code != -32602 || errorCode != "too_large" {
		t.Fatalf("oversized blob = %d/%q", code, errorCode)
	}
}
