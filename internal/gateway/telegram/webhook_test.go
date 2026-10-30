package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testWebhookSecret = "s3cret_Token-1" // #nosec G101 -- test fixture, not a credential

// webhookFixture builds an adapter plus a handler with a 1 KiB body limit.
func webhookFixture(t *testing.T) (*fakeTG, *Adapter, http.Handler, chan Inbound) {
	t.Helper()
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	in := make(chan Inbound, 8)
	h, err := a.WebhookHandler(WebhookOptions{Secret: testWebhookSecret, In: in, MaxBody: 1024})
	if err != nil {
		t.Fatalf("WebhookHandler: %v", err)
	}
	return f, a, h, in
}

func webhookBody(updateID int64, text string) string {
	const chat = 7
	return fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"from":{"id":%d},"chat":{"id":%d},"text":%q}}`,
		updateID, updateID, chat, chat, text)
}

func postWebhook(h http.Handler, secret, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/telegram/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set(WebhookSecretHeader, secret)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestWebhookRejectsMissingOrWrongSecret(t *testing.T) {
	_, _, h, in := webhookFixture(t)
	body := webhookBody(1, "halo")
	for _, secret := range []string{"", "wrong", testWebhookSecret + "x"} {
		if rec := postWebhook(h, secret, body); rec.Code != http.StatusForbidden {
			t.Errorf("secret %q: status = %d, want 403", secret, rec.Code)
		}
	}
	select {
	case ib := <-in:
		t.Fatalf("unauthorized update delivered: %+v", ib)
	default:
	}
}

func TestWebhookRejectsOversizedBody(t *testing.T) {
	_, _, h, in := webhookFixture(t)
	body := webhookBody(1, strings.Repeat("x", 2048))
	rec := postWebhook(h, testWebhookSecret, body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	select {
	case ib := <-in:
		t.Fatalf("oversized update delivered: %+v", ib)
	default:
	}
}

func TestWebhookDuplicateUpdateDeliveredOnce(t *testing.T) {
	_, _, h, in := webhookFixture(t)
	body := webhookBody(9, "sekali")
	if rec := postWebhook(h, testWebhookSecret, body); rec.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", rec.Code)
	}
	ib := recvInbound(t, in)
	if ib.UpdateID != "u:9" || ib.ChatID != "7" || ib.Text != "sekali" {
		t.Fatalf("inbound = %+v", ib)
	}
	if rec := postWebhook(h, testWebhookSecret, body); rec.Code != http.StatusOK {
		t.Fatalf("duplicate status = %d, want 200", rec.Code)
	}
	select {
	case got := <-in:
		t.Fatalf("duplicate delivered again: %+v", got)
	default:
	}
	// A different id still flows after the duplicate was swallowed.
	if rec := postWebhook(h, testWebhookSecret, webhookBody(10, "kedua")); rec.Code != http.StatusOK {
		t.Fatalf("next status = %d, want 200", rec.Code)
	}
	if got := recvInbound(t, in); got.UpdateID != "u:10" {
		t.Fatalf("next inbound = %+v", got)
	}
}

func TestWebhookRejectsInvalidPayload(t *testing.T) {
	_, _, h, in := webhookFixture(t)
	for _, body := range []string{`{}`, `{"update_id":0}`, `not json`, `{"update_id":1}{"update_id":2}`} {
		if rec := postWebhook(h, testWebhookSecret, body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rec.Code)
		}
	}
	select {
	case ib := <-in:
		t.Fatalf("invalid update delivered: %+v", ib)
	default:
	}
}

func TestSetDeleteWebhookNeverDropsUpdates(t *testing.T) {
	f, a, _, _ := webhookFixture(t)
	ctx := context.Background()
	if err := a.SetWebhook(ctx, "https://gateway.example/telegram/webhook", testWebhookSecret); err != nil {
		t.Fatalf("SetWebhook: %v", err)
	}
	calls := f.callsFor("setWebhook")
	if len(calls) != 1 {
		t.Fatalf("setWebhook calls = %d, want 1", len(calls))
	}
	if got := calls[0].Form.Get("url"); got != "https://gateway.example/telegram/webhook" {
		t.Errorf("url = %q", got)
	}
	if got := calls[0].Form.Get("secret_token"); got != testWebhookSecret {
		t.Errorf("secret_token = %q", got)
	}
	if got := calls[0].Form.Get("drop_pending_updates"); got != "" {
		t.Errorf("setWebhook sent drop_pending_updates=%q", got)
	}
	if err := a.DeleteWebhook(ctx); err != nil {
		t.Fatalf("DeleteWebhook: %v", err)
	}
	calls = f.callsFor("deleteWebhook")
	if len(calls) != 1 {
		t.Fatalf("deleteWebhook calls = %d, want 1", len(calls))
	}
	if got := calls[0].Form.Get("drop_pending_updates"); got != "" {
		t.Errorf("deleteWebhook sent drop_pending_updates=%q", got)
	}
	if err := a.SetWebhook(ctx, "https://gateway.example/hook", "bad secret"); !errors.Is(err, ErrWebhookSecret) {
		t.Errorf("invalid secret error = %v, want ErrWebhookSecret", err)
	}
	if err := a.SetWebhook(ctx, "", testWebhookSecret); !errors.Is(err, ErrWebhookURL) {
		t.Errorf("empty url error = %v, want ErrWebhookURL", err)
	}
}

// TestWebhookAfterPollingDelivers proves the poll→webhook transition reuses
// the same adapter and inbound path: an update polled before the switch and
// one posted after it both arrive on in.
func TestWebhookAfterPollingDelivers(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	in := make(chan Inbound, 8)
	done, stop := runAdapter(t, a, in)
	f.pushText(t, 7, 0, "poll")
	if ib := recvInbound(t, in); ib.Text != "poll" || ib.UpdateID != "u:1" {
		t.Fatalf("polled inbound = %+v", ib)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	h, err := a.WebhookHandler(WebhookOptions{Secret: testWebhookSecret, In: in})
	if err != nil {
		t.Fatalf("WebhookHandler: %v", err)
	}
	body := webhookBody(2, "webhook")
	if rec := postWebhook(h, testWebhookSecret, body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ib := recvInbound(t, in); ib.Text != "webhook" || ib.UpdateID != "u:2" {
		t.Fatalf("webhook inbound = %+v", ib)
	}
}

func TestWebhookTimeoutReleasesUpdate(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	h, err := a.WebhookHandler(WebhookOptions{Secret: testWebhookSecret, In: make(chan Inbound), Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("WebhookHandler: %v", err)
	}
	body := webhookBody(3, "blocked")
	if rec := postWebhook(h, testWebhookSecret, body); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	// The failed id was released: a retry is admitted, not treated as a
	// duplicate, and the body is validated before the channel blocks.
	if rec := postWebhook(h, testWebhookSecret, body); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("retry status = %d, want 503", rec.Code)
	}
}
