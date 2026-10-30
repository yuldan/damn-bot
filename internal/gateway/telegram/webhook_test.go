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

var errCommit = errors.New("fake inbox write failure")

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

type webhookCall struct {
	rec  *httptest.ResponseRecorder
	done chan struct{}
}

func startWebhook(h http.Handler, secret, body string) *webhookCall {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/telegram/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set(WebhookSecretHeader, secret)
	}
	c := &webhookCall{rec: httptest.NewRecorder(), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		h.ServeHTTP(c.rec, req)
	}()
	return c
}

func (c *webhookCall) wait(t *testing.T) int {
	t.Helper()
	select {
	case <-c.done:
		return c.rec.Code
	case <-time.After(3 * time.Second):
		t.Fatal("webhook handler did not return")
		return 0
	}
}

// postWebhook runs one request as both the transport and the gate: it takes
// the delivered Inbound (if any) and reports commitErr on its Commit callback.
func postWebhook(t *testing.T, h http.Handler, in <-chan Inbound, secret, body string, commitErr error) int {
	t.Helper()
	c := startWebhook(h, secret, body)
	select {
	case ib := <-in:
		if ib.Commit == nil {
			t.Fatal("inbound has no Commit callback")
		}
		ib.Commit(commitErr)
	case <-c.done:
	case <-time.After(3 * time.Second):
		t.Fatal("webhook handler neither committed nor returned")
	}
	return c.wait(t)
}

func TestWebhookRejectsMissingOrWrongSecret(t *testing.T) {
	_, _, h, in := webhookFixture(t)
	body := webhookBody(1, "halo")
	for _, secret := range []string{"", "wrong", testWebhookSecret + "x"} {
		if code := startWebhook(h, secret, body).wait(t); code != http.StatusForbidden {
			t.Errorf("secret %q: status = %d, want 403", secret, code)
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
	if code := startWebhook(h, testWebhookSecret, body).wait(t); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", code)
	}
	select {
	case ib := <-in:
		t.Fatalf("oversized update delivered: %+v", ib)
	default:
	}
}

func TestWebhookWaitsForCommitBefore200(t *testing.T) {
	_, _, h, in := webhookFixture(t)
	c := startWebhook(h, testWebhookSecret, webhookBody(4, "commit"))
	ib := recvInbound(t, in)
	select {
	case <-c.done:
		t.Fatal("handler answered before the inbound was committed")
	case <-time.After(100 * time.Millisecond):
	}
	ib.Commit(nil)
	if code := c.wait(t); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
}

func TestWebhookCommitFailureIsRetryable(t *testing.T) {
	_, _, h, in := webhookFixture(t)
	body := webhookBody(5, "retry")
	if code := postWebhook(t, h, in, testWebhookSecret, body, errCommit); code != http.StatusServiceUnavailable {
		t.Fatalf("failed commit status = %d, want 503", code)
	}
	// The failed id was released: the same update is delivered again and the
	// retry's successful commit gets the 200.
	if code := postWebhook(t, h, in, testWebhookSecret, body, nil); code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200", code)
	}
}

func TestWebhookDuplicateUpdateDeliveredOnce(t *testing.T) {
	_, _, h, in := webhookFixture(t)
	body := webhookBody(9, "sekali")
	c := startWebhook(h, testWebhookSecret, body)
	ib := recvInbound(t, in)
	if ib.UpdateID != "u:9" || ib.ChatID != "7" || ib.Text != "sekali" {
		t.Fatalf("inbound = %+v", ib)
	}
	ib.Commit(nil)
	if code := c.wait(t); code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", code)
	}
	if code := startWebhook(h, testWebhookSecret, body).wait(t); code != http.StatusOK {
		t.Fatalf("duplicate status = %d, want 200", code)
	}
	select {
	case got := <-in:
		t.Fatalf("duplicate delivered again: %+v", got)
	default:
	}
	// A different id still flows after the duplicate was swallowed.
	c = startWebhook(h, testWebhookSecret, webhookBody(10, "kedua"))
	next := recvInbound(t, in)
	if next.UpdateID != "u:10" {
		t.Fatalf("next inbound = %+v", next)
	}
	next.Commit(nil)
	if code := c.wait(t); code != http.StatusOK {
		t.Fatalf("next status = %d, want 200", code)
	}
}

func TestWebhookRejectsInvalidPayload(t *testing.T) {
	_, _, h, in := webhookFixture(t)
	for _, body := range []string{`{}`, `{"update_id":0}`, `not json`, `{"update_id":1}{"update_id":2}`} {
		if code := startWebhook(h, testWebhookSecret, body).wait(t); code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, code)
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
	c := startWebhook(h, testWebhookSecret, webhookBody(2, "webhook"))
	ib := recvInbound(t, in)
	if ib.Text != "webhook" || ib.UpdateID != "u:2" {
		t.Fatalf("webhook inbound = %+v", ib)
	}
	ib.Commit(nil)
	if code := c.wait(t); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
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
	for range 2 {
		if code := startWebhook(h, testWebhookSecret, body).wait(t); code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", code)
		}
	}
}
