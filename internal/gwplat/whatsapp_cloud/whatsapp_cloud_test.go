package whatsapp_cloud_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/outq"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/gwplat/whatsapp_cloud"
)

const (
	testPhone  = "1234567890"
	testToken  = "EAAG-test-access-token"
	testSecret = "app-secret-value"
	testVerify = "verify-token-value"
)

type secrets map[string]string

func (s secrets) Get(_ context.Context, name string) (string, error) {
	value, ok := s[name]
	if !ok {
		return "", errors.New("missing")
	}
	return value, nil
}

func testSecrets() secrets {
	return secrets{
		"whatsapp_cloud_access_token": testToken,
		"whatsapp_cloud_app_secret":   testSecret,
		"whatsapp_cloud_verify_token": testVerify,
	}
}

type allowPacer struct{}

func (allowPacer) TakeChat(string, string, bool, time.Time) (bool, time.Duration) { return true, 0 }
func (allowPacer) Penalize(string, time.Time)                                     {}

func testOutbox(t *testing.T, d *gwplat.Descriptor) *gwplat.Outbox {
	t.Helper()
	sched, err := outq.New(&outq.Options{Pacer: allowPacer{}, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		if runErr := sched.Run(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Errorf("scheduler: %v", runErr)
		}
	}()
	out, err := gwplat.NewOutbox(d, sched)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func newAdapter(t *testing.T, h gwplat.Handler, now *time.Time) *whatsapp_cloud.Adapter {
	t.Helper()
	d := descriptor(t)
	a, err := whatsapp_cloud.New(t.Context(), &whatsapp_cloud.Config{
		Enabled:       true,
		PhoneNumberID: testPhone,
		Now:           func() time.Time { return *now },
	}, &d, testSecrets(), testOutbox(t, &d), h)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func webhookBody(t *testing.T, id, from, text string, at time.Time) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"object": "whatsapp_business_account",
		"entry": []any{map[string]any{
			"id": "biz",
			"changes": []any{map[string]any{
				"field": "messages",
				"value": map[string]any{
					"messaging_product": "whatsapp",
					"metadata":          map[string]any{"phone_number_id": testPhone},
					"messages": []any{map[string]any{
						"from": from, "id": id, "timestamp": fmt.Sprint(at.Unix()),
						"type": "text", "text": map[string]any{"body": text},
					}},
				},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("marshal webhook: %v", err)
	}
	return body
}

func sign(body []byte) string {
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func header(sig string) http.Header {
	h := http.Header{}
	h.Set(whatsapp_cloud.SignatureHeader, sig)
	return h
}

// TestF7WACloudSignature is the negative control of the webhook boundary: a
// bad or missing X-Hub-Signature-256 admits zero messages, a tampered body is
// rejected, a valid signature admits exactly one, and the secret never leaks
// into the error text.
func TestF7WACloudSignature(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var handled []*gateway.Inbound
	h := gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error {
		handled = append(handled, in)
		return nil
	})
	a := newAdapter(t, h, &now)
	body := webhookBody(t, "wamid.sig", "628222", "hi", now)

	for _, tc := range []struct {
		name string
		hdr  http.Header
	}{
		{"missing", http.Header{}},
		{"garbage", header("sha256=zz")},
		{"wrong", header(sign([]byte("other body")))},
		{"sha1", header("sha1=deadbeef")},
	} {
		n, err := a.HandleWebhook(t.Context(), tc.hdr, body)
		if !errors.Is(err, whatsapp_cloud.ErrSignature) || n != 0 {
			t.Fatalf("%s signature = %d/%v, want ErrSignature and 0", tc.name, n, err)
		}
		if strings.Contains(err.Error(), testSecret) || strings.Contains(err.Error(), testToken) {
			t.Fatalf("%s: error leaked a secret: %v", tc.name, err)
		}
	}
	if len(handled) != 0 {
		t.Fatalf("unsigned webhook dispatched %d messages", len(handled))
	}
	if n, err := a.HandleWebhook(t.Context(), header(sign(body)), body); err != nil || n != 1 || len(handled) != 1 {
		t.Fatalf("signed webhook = %d/%v handled=%d", n, err, len(handled))
	}
	if in := handled[0]; in.Platform != "whatsapp_cloud" || in.ChatID != "628222" || in.Text != "hi" || in.UpdateID != "wamid.sig" {
		t.Fatalf("inbound fields = %+v", in)
	}
	if n, err := a.HandleWebhook(t.Context(), header(sign(body)), body); err != nil || n != 0 || len(handled) != 1 {
		t.Fatalf("replay = %d/%v handled=%d", n, err, len(handled))
	}
}

// TestWhatsAppCloudInboundAdmission proves explicit activation, the GET
// handshake, and the inbound media ceiling before anything is downloaded.
func TestWhatsAppCloudInboundAdmission(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil })
	a := newAdapter(t, h, &now)
	d := descriptor(t)

	if _, err := whatsapp_cloud.New(t.Context(), &whatsapp_cloud.Config{}, &d, testSecrets(), nil, h); !errors.Is(err, gwplat.ErrUnavailable) {
		t.Fatalf("disabled = %v, want ErrUnavailable", err)
	}
	if _, err := whatsapp_cloud.New(t.Context(), &whatsapp_cloud.Config{Enabled: true, PhoneNumberID: testPhone}, &d, secrets{}, a.Outbox(), h); !errors.Is(err, gwplat.ErrMissingSecret) {
		t.Fatalf("missing secret = %v, want ErrMissingSecret", err)
	}
	if _, err := whatsapp_cloud.New(t.Context(), &whatsapp_cloud.Config{Enabled: true, PhoneNumberID: testPhone + "/../x"}, &d, testSecrets(), a.Outbox(), h); !errors.Is(err, whatsapp_cloud.ErrConfig) {
		t.Fatalf("malformed phone id = %v, want ErrConfig", err)
	}
	if _, err := a.Verify("subscribe", "wrong", "challenge"); !errors.Is(err, whatsapp_cloud.ErrVerify) {
		t.Fatalf("wrong verify token = %v", err)
	}
	if got, err := a.Verify("subscribe", testVerify, "challenge-123"); err != nil || got != "challenge-123" {
		t.Fatalf("handshake = %q/%v", got, err)
	}

	oversize := strings.Replace(string(webhookBody(t, "wamid.media", "628111", "pic", now)),
		`"type":"text"`, `"type":"image","image":{"id":"m2","mime_type":"image/jpeg","file_size":6291456}`, 1)
	if _, err := a.HandleWebhook(t.Context(), header(sign([]byte(oversize))), []byte(oversize)); !errors.Is(err, whatsapp_cloud.ErrMediaTooLarge) {
		t.Fatalf("oversize inbound = %v, want ErrMediaTooLarge", err)
	}
	if _, err := a.HandleWebhook(t.Context(), header(sign(make([]byte, whatsapp_cloud.MaxWebhookBytes+1))), make([]byte, whatsapp_cloud.MaxWebhookBytes+1)); !errors.Is(err, whatsapp_cloud.ErrWebhookTooBig) {
		t.Fatalf("oversize body = %v, want ErrWebhookTooBig", err)
	}
}

func descriptor(t *testing.T) gwplat.Descriptor {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	d, err := reg.Lookup("whatsapp_cloud")
	if err != nil {
		t.Fatal(err)
	}
	return d
}
