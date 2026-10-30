//go:build unix

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

const webhookTestSecret = "e2e_secret-Token_1" // #nosec G101 -- test fixture, not a credential

var webhookAddrRE = regexp.MustCompile(`telegram webhook listening on ([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+:[0-9]+)`)

// webhookServeEnv switches the real serve binary to webhook mode on an
// ephemeral loopback port. The public URL is only registered with the fake
// Bot API, which records it instead of calling back.
func webhookServeEnv(e *Env, tg *FakeTG) []string {
	return append(gatewayServeEnv(e, tg),
		"DAMNBOT_TELEGRAM_MODE=webhook",
		"DAMNBOT_TELEGRAM_WEBHOOK_LISTEN=127.0.0.1:0",
		"DAMNBOT_TELEGRAM_WEBHOOK_URL=https://gateway.example/telegram/webhook",
		"DAMNBOT_TELEGRAM_WEBHOOK_SECRET="+webhookTestSecret,
	)
}

func startWebhookServe(t *testing.T, e *Env, tg *FakeTG) (*serveProc, string) {
	t.Helper()
	p := startServe(t, e, binPaths.main, webhookServeEnv(e, tg))
	p.awaitLog(p.stderr, "telegram webhook listening on", 10*time.Second)
	m := webhookAddrRE.FindStringSubmatch(p.stderr.String())
	if m == nil {
		t.Fatalf("webhook address not found in stderr:\n%s", p.stderr.String())
	}
	return p, "http://" + m[1] + "/telegram/webhook"
}

func webhookUpdate(t *testing.T, updateID int64, text string) []byte {
	chatID := int64(gatewayOwner)
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"update_id": updateID,
		"message": map[string]any{
			"message_id": updateID,
			"from":       map[string]any{"id": chatID, "first_name": "owner"},
			"chat":       map[string]any{"id": chatID, "type": "private"},
			"date":       time.Now().Unix(),
			"text":       text,
		},
	})
	if err != nil {
		t.Fatalf("marshal webhook update: %v", err)
	}
	return body
}

func postWebhook(t *testing.T, target, secret string, body []byte) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build webhook request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("post webhook: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // test client; status is the assertion
	if _, cerr := io.Copy(io.Discard, resp.Body); cerr != nil {
		t.Fatalf("drain webhook response: %v", cerr)
	}
	return resp.StatusCode
}

// TestServeTelegramWebhookEndToEnd drives the real serve binary in webhook
// mode: a forged POST runs one turn, a wrong secret is rejected without a
// turn, and a duplicate update is acknowledged but never runs twice.
func TestServeTelegramWebhookEndToEnd(t *testing.T) {
	e := New(t, providertest.Text("pong"), providertest.Text("pong"))
	tg := NewFakeTG(t, FakeTGOptions{})
	p, target := startWebhookServe(t, e, tg)
	waitFor(t, 10*time.Second, "setWebhook", func() bool { return len(tg.RequestsFor("setWebhook")) > 0 })

	// Mode exclusivity: webhook mode never starts a getUpdates poller.
	<-time.After(300 * time.Millisecond)
	if n := len(tg.RequestsFor("getUpdates")); n != 0 {
		t.Fatalf("getUpdates calls in webhook mode = %d, want 0", n)
	}

	if code := postWebhook(t, target, webhookTestSecret, webhookUpdate(t, 101, "halo")); code != http.StatusOK {
		t.Fatalf("first webhook status = %d, want 200", code)
	}
	waitFor(t, 15*time.Second, "turn reply", func() bool {
		for _, edit := range tg.Edits() {
			if edit.Text == "pong" {
				return true
			}
		}
		return false
	})
	if got := len(e.Requests()); got != 1 {
		t.Fatalf("provider requests after one webhook update = %d, want 1", got)
	}

	// Negative control: a wrong secret is refused and mutates nothing.
	before := len(e.Requests())
	if code := postWebhook(t, target, "wrong-secret", webhookUpdate(t, 102, "steal")); code != http.StatusForbidden {
		t.Fatalf("wrong secret status = %d, want 403", code)
	}
	<-time.After(300 * time.Millisecond)
	if got := len(e.Requests()); got != before {
		t.Fatalf("provider requests after rejected secret = %d, want %d", got, before)
	}

	// A duplicate update is acked but runs no second turn.
	if code := postWebhook(t, target, webhookTestSecret, webhookUpdate(t, 103, "sekali")); code != http.StatusOK {
		t.Fatalf("duplicate first status = %d, want 200", code)
	}
	waitFor(t, 15*time.Second, "duplicate turn", func() bool { return len(e.Requests()) == before+1 })
	if code := postWebhook(t, target, webhookTestSecret, webhookUpdate(t, 103, "sekali")); code != http.StatusOK {
		t.Fatalf("duplicate second status = %d, want 200", code)
	}
	<-time.After(700 * time.Millisecond)
	if got := len(e.Requests()); got != before+1 {
		t.Fatalf("provider requests after duplicate = %d, want %d", got, before+1)
	}

	gatewayStop(t, p)
	if n := len(tg.RequestsFor("deleteWebhook")); n == 0 {
		t.Error("deleteWebhook was not called on planned stop")
	}
	db := gatewayDB(t, e)
	for _, tc := range []struct {
		update string
		want   int64
	}{{"u:101", 1}, {"u:102", 0}, {"u:103", 1}} {
		query := fmt.Sprintf(`SELECT count(*) FROM gateway_inbound WHERE external_id = '%s'`, tc.update)
		if n := gatewayCount(t, db, query); n != tc.want {
			t.Errorf("%s rows = %d, want %d", tc.update, n, tc.want)
		}
	}
}
