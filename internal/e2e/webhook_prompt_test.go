//go:build unix

package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

const genericWebhookSecret = "e2e-webhook-secret" // #nosec G101 -- test fixture, not a credential

var genericWebhookAddrRE = regexp.MustCompile(`gateway: webhook listening on ([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+:[0-9]+)`)

// genericWebhookServeEnv enables the generic receiver on an ephemeral loopback
// port; the webhook sender is the owner identity so no role-user workspace is
// needed for this prompt path.
func genericWebhookServeEnv(e *Env, tg *FakeTG) []string {
	env := gatewayServeEnv(e, tg)
	for i := range env {
		if strings.HasPrefix(env[i], "DAMNBOT_GATEWAY_OWNER=") {
			env[i] = "DAMNBOT_GATEWAY_OWNER=webhook:e2e"
		}
	}
	return append(env,
		"DAMNBOT_WEBHOOK_LISTEN=127.0.0.1:0",
		"DAMNBOT_WEBHOOK_SECRET="+genericWebhookSecret,
	)
}

func startGenericWebhookServe(t *testing.T, e *Env, tg *FakeTG) (*serveProc, string) {
	t.Helper()
	p := startServe(t, e, binPaths.main, genericWebhookServeEnv(e, tg))
	p.awaitLog(p.stderr, "gateway: webhook listening on", 10*time.Second)
	m := genericWebhookAddrRE.FindStringSubmatch(p.stderr.String())
	if m == nil {
		t.Fatalf("webhook address not found in stderr:\n%s", p.stderr.String())
	}
	return p, "http://" + m[1] + "/webhook"
}

func webhookEnvelope(t *testing.T, id, text string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id": id, "event": "message.created", "chat": "webhook-room",
		"sender": "e2e", "text": text, "mentioned": true,
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return body
}

func signWebhook(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func postGenericWebhook(t *testing.T, client interface {
	Do(*http.Request) (*http.Response, error)
}, target, secret string, body []byte,
) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build webhook request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-Webhook-Signature", signWebhook(body, secret))
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post webhook: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // test client; status is the assertion
	if _, cerr := io.Copy(io.Discard, resp.Body); cerr != nil {
		t.Fatalf("drain webhook response: %v", cerr)
	}
	return resp.StatusCode
}

// TestF7WebhookPromptE2E (PLAT-029): a signed delivery to the real serve
// binary runs one prompt turn; a bad signature never runs one, and a replay of
// the same delivery id is acknowledged without a second turn.
func TestF7WebhookPromptE2E(t *testing.T) {
	e := New(t, providertest.Text("pong from webhook"))
	tg := NewFakeTG(t, FakeTGOptions{})
	p, target := startGenericWebhookServe(t, e, tg)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("serve stderr:\n%s", p.stderr.String())
		}
	})
	client := netx.New(netx.Config{}).Client("refs", netx.Tool)

	body := webhookEnvelope(t, "e2e-1", "hello webhook")
	if code := postGenericWebhook(t, client, target, genericWebhookSecret, body); code != http.StatusOK {
		t.Fatalf("signed webhook status = %d, want 200", code)
	}
	waitFor(t, 20*time.Second, "turn", func() bool {
		for _, raw := range e.Requests() {
			if strings.Contains(string(raw), "hello webhook") {
				return true
			}
		}
		return false
	})

	if code := postGenericWebhook(t, client, target, genericWebhookSecret, body); code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200", code)
	}
	if code := postGenericWebhook(t, client, target, "wrong-secret", body); code != http.StatusUnauthorized {
		t.Fatalf("unsigned webhook status = %d, want 401", code)
	}
	if n := len(e.Requests()); n != 1 {
		t.Fatalf("provider requests = %d, want exactly 1 after replay and rejection", n)
	}
	gatewayStop(t, p)
}
