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

	"github.com/damnbot/damnbot/internal/gwapi/webhook"
)

const cliTestSecret = "s3cr3t-value" // #nosec G101 -- test fixture, not a credential

type webhookCLIFixture struct {
	t           *testing.T
	srv         *httptest.Server
	dir         string
	calls       atomic.Int32
	signatureOK atomic.Bool
}

func newWebhookCLIFixture(t *testing.T) *webhookCLIFixture {
	t.Helper()
	f := &webhookCLIFixture{t: t, dir: t.TempDir()}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read: %v", err)
		}
		f.signatureOK.Store(r.Header.Get(webhook.SignatureHeader) == webhook.Sign([]byte(cliTestSecret), body))
		var env webhook.Envelope
		if uerr := json.Unmarshal(body, &env); uerr != nil {
			t.Errorf("envelope: %v", uerr)
		}
		if env.Event != "test" || env.ID == "" {
			t.Errorf("test envelope = %+v", env)
		}
		f.calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *webhookCLIFixture) run(args ...string) (int, string, string) {
	f.t.Helper()
	secretGet := func(context.Context, string, string, string) (string, error) { return cliTestSecret, nil }
	doerFor := func(string, bool) (webhook.HTTPDoer, error) { return f.srv.Client(), nil }
	var out, errs bytes.Buffer
	code := runWebhookWith(args, &out, &errs, secretGet, doerFor)
	return code, out.String(), errs.String()
}

func (f *webhookCLIFixture) storeIDs() []string {
	f.t.Helper()
	store, err := webhook.OpenStore(filepath.Join(f.dir, "webhook", "subscriptions.json"), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	subs := store.List()
	ids := make([]string, 0, len(subs))
	for i := range subs {
		ids = append(ids, subs[i].ID)
	}
	return ids
}

// TestF7CLI026Webhook (CLI-026): subscribe/list/remove/test over the
// persistent subscription store; test sends exactly one signed event and no
// command ever prints the secret value.
func TestF7CLI026Webhook(t *testing.T) {
	f := newWebhookCLIFixture(t)
	id := f.subscribeAndList()
	f.sendTest(id)
	f.removeAndVerify(id)
	if entry, ok := lookupSurface("webhook"); !ok || entry.Run == nil || entry.Phase != phaseF7 {
		t.Fatalf("webhook surface = %+v, ok=%t", entry, ok)
	}
}

func (f *webhookCLIFixture) subscribeAndList() string {
	f.t.Helper()
	code, out, errs := f.run("subscribe", "--url", f.srv.URL, "--event", "message.created",
		"--secret", "webhook_secret", "--data-dir", f.dir)
	if code != 0 || !strings.Contains(out, "added wh_") || strings.Contains(out+errs, cliTestSecret) {
		f.t.Fatalf("subscribe = %d %q %q", code, out, errs)
	}
	ids := f.storeIDs()
	if len(ids) != 1 {
		f.t.Fatalf("store rows = %d, want 1", len(ids))
	}
	id := ids[0]
	if code, out, _ = f.run("subscribe", "--url", f.srv.URL, "--event", "message.created",
		"--secret", "webhook_secret", "--data-dir", f.dir); code != 0 || !strings.Contains(out, "exists "+id) {
		f.t.Fatalf("idempotent subscribe = %d %q", code, out)
	}
	if len(f.storeIDs()) != 1 {
		f.t.Fatal("idempotent subscribe created a second row")
	}
	code, out, _ = f.run("list", "--data-dir", f.dir)
	if code != 0 || !strings.Contains(out, id) || !strings.Contains(out, f.srv.URL) ||
		!strings.Contains(out, "events=message.created") || !strings.Contains(out, "secret=webhook_secret") {
		f.t.Fatalf("list = %d %q", code, out)
	}
	if strings.Contains(out, cliTestSecret) {
		f.t.Fatalf("list leaked the secret: %q", out)
	}
	return id
}

func (f *webhookCLIFixture) sendTest(id string) {
	f.t.Helper()
	code, out, errs := f.run("test", id, "--data-dir", f.dir)
	if code != 0 || f.calls.Load() != 1 || !f.signatureOK.Load() {
		f.t.Fatalf("test = %d %q %q calls=%d signed=%t", code, out, errs, f.calls.Load(), f.signatureOK.Load())
	}
	if strings.Contains(out+errs, cliTestSecret) {
		f.t.Fatalf("test leaked the secret: %q", out+errs)
	}
}

func (f *webhookCLIFixture) removeAndVerify(id string) {
	f.t.Helper()
	if code, out, _ := f.run("remove", id, "--data-dir", f.dir); code != 0 || !strings.Contains(out, "removed "+id) {
		f.t.Fatalf("remove = %d %q", code, out)
	}
	if code, out, _ := f.run("list", "--data-dir", f.dir); code != 0 || !strings.Contains(out, "no webhook subscriptions") {
		f.t.Fatalf("list after remove = %d %q", code, out)
	}
	if code, _, _ := f.run("remove", id, "--data-dir", f.dir); code != 1 {
		f.t.Fatalf("remove unknown = %d, want 1", code)
	}
}
