//go:build !minimal

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/damnbot/damnbot/internal/gwplat/webhook"
)

// TestF7CLI026Webhook (CLI-026): add/list/remove/test over the persistent
// subscription store; test sends exactly one signed event and no command ever
// prints the secret value.
func TestF7CLI026Webhook(t *testing.T) {
	var calls atomic.Int32
	var signatureOK atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read: %v", err)
		}
		signatureOK.Store(r.Header.Get(webhook.SignatureHeader) == webhook.Sign([]byte("s3cr3t-value"), body))
		var env webhook.Envelope
		if uerr := json.Unmarshal(body, &env); uerr != nil {
			t.Errorf("envelope: %v", uerr)
		}
		if env.Event != "test" || env.ID == "" {
			t.Errorf("test envelope = %+v", env)
		}
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	secretGet := func(context.Context, string, string, string) (string, error) {
		return "s3cr3t-value", nil
	}

	dir := t.TempDir()
	run := func(args ...string) (int, string, string) {
		t.Helper()
		var out, errs bytes.Buffer
		code := runWebhookWith(args, &out, &errs, secretGet)
		return code, out.String(), errs.String()
	}
	code, out, errs := run("add", "--url", srv.URL, "--event", "message.created",
		"--secret", "webhook_secret", "--data-dir", dir)
	if code != 0 || !strings.Contains(out, "added wh_") || strings.Contains(out+errs, "s3cr3t-value") {
		t.Fatalf("add = %d %q %q", code, out, errs)
	}
	store, err := webhook.OpenStore(filepath.Join(dir, "webhook", "subscriptions.json"), nil)
	if err != nil || len(store.List()) != 1 {
		t.Fatalf("store = %v, %v", store, err)
	}
	id := store.List()[0].ID

	code, out, _ = run("add", "--url", srv.URL, "--event", "message.created",
		"--secret", "webhook_secret", "--data-dir", dir)
	if code != 0 || !strings.Contains(out, "exists "+id) {
		t.Fatalf("idempotent add = %d %q", code, out)
	}
	if len(store.List()) != 1 {
		t.Fatal("idempotent add created a second row")
	}

	code, out, _ = run("list", "--data-dir", dir)
	if code != 0 || !strings.Contains(out, id) || !strings.Contains(out, srv.URL) ||
		!strings.Contains(out, "events=message.created") || !strings.Contains(out, "secret=webhook_secret") {
		t.Fatalf("list = %d %q", code, out)
	}
	if strings.Contains(out, "s3cr3t-value") {
		t.Fatalf("list leaked the secret: %q", out)
	}

	code, out, errs = run("test", id, "--data-dir", dir)
	if code != 0 || calls.Load() != 1 || !signatureOK.Load() {
		t.Fatalf("test = %d %q %q calls=%d signed=%t", code, out, errs, calls.Load(), signatureOK.Load())
	}
	if strings.Contains(out+errs, "s3cr3t-value") {
		t.Fatalf("test leaked the secret: %q", out+errs)
	}

	if code, out, _ = run("remove", id, "--data-dir", dir); code != 0 || !strings.Contains(out, "removed "+id) {
		t.Fatalf("remove = %d %q", code, out)
	}
	if code, out, _ = run("list", "--data-dir", dir); code != 0 || !strings.Contains(out, "no webhook subscriptions") {
		t.Fatalf("list after remove = %d %q", code, out)
	}
	if code, _, _ = run("remove", id, "--data-dir", dir); code != 1 {
		t.Fatalf("remove unknown = %d, want 1", code)
	}
	if entry, ok := lookupSurface("webhook"); !ok || entry.Run == nil || entry.Phase != phaseF7 {
		t.Fatalf("webhook surface = %+v, ok=%t", entry, ok)
	}
}
