//go:build unix

package e2e

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// webhookSignature is the documented wire form of the receiver header; the
// e2e package deliberately does not import the adapter package (import matrix).
func webhookSignature(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve addr: %v", err)
	}
	addr := ln.Addr().String()
	if cerr := ln.Close(); cerr != nil {
		t.Fatalf("release addr: %v", cerr)
	}
	return addr
}

// TestF7E2EWebhookPrompt: the real binary serves the generic webhook, admits
// one signed delivery, and runs exactly one provider turn; a replay of the same
// envelope adds no second turn.
func TestF7E2EWebhookPrompt(t *testing.T) {
	e := New(t, providertest.Text("webhook answer"))
	tg := NewFakeTG(t, FakeTGOptions{})
	addr := freeLoopbackAddr(t)
	env := gatewayServeEnv(e, tg)
	for i := range env {
		if strings.HasPrefix(env[i], "DAMNBOT_GATEWAY_OWNER=") {
			env[i] = "DAMNBOT_GATEWAY_OWNER=webhook:alice"
		}
	}
	env = append(env,
		"DAMNBOT_WEBHOOK_LISTEN="+addr,
		"DAMNBOT_WEBHOOK_SECRET=e2e-webhook-secret",
	)
	p := startServe(t, e, binPaths.main, env)

	waitFor(t, 15*time.Second, "the webhook listener", func() bool {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return false
		}
		return conn.Close() == nil
	})

	body, err := json.Marshal(map[string]any{
		"id": "wh-e2e-1", "event": "message.created", "chat": "smoke",
		"sender": "alice", "text": "hello from the webhook",
	})
	if err != nil {
		t.Fatal(err)
	}
	post := func() int {
		req, rerr := http.NewRequest(http.MethodPost, "http://"+addr+"/webhook", bytes.NewReader(body))
		if rerr != nil {
			t.Fatalf("request: %v", rerr)
		}
		req.Header.Set("X-Webhook-Signature", webhookSignature([]byte("e2e-webhook-secret"), body))
		resp, derr := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if derr != nil {
			return 0
		}
		defer resp.Body.Close()
		if _, cerr := io.Copy(io.Discard, resp.Body); cerr != nil {
			t.Fatalf("drain: %v", cerr)
		}
		return resp.StatusCode
	}
	if code := post(); code != http.StatusOK {
		t.Fatalf("webhook POST = %d, want 200", code)
	}
	deadline := time.Now().Add(20 * time.Second)
	for len(e.Server.Requests()) != 1 && time.Now().Before(deadline) {
		<-time.After(50 * time.Millisecond)
	}
	if len(e.Server.Requests()) != 1 {
		t.Fatalf("webhook turn missing; requests=%d\nstderr:\n%s\nstdout:\n%s",
			len(e.Server.Requests()), p.stderr.String(), p.stdout.String())
	}
	if code := post(); code != http.StatusOK {
		t.Fatalf("replayed webhook = %d, want 200", code)
	}
	<-time.After(500 * time.Millisecond)
	if got := len(e.Server.Requests()); got != 1 {
		t.Fatalf("provider requests after replay = %d, want 1", got)
	}
}
