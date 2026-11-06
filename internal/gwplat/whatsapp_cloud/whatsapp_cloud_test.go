package whatsapp_cloud_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// recorder is the fake Graph endpoint: it records each POST body, signals the
// outbox test, and returns an error shape when configured.
type recorder struct {
	mu       sync.Mutex
	posts    []map[string]any
	gets     []string
	sent     chan struct{}
	status   int
	response string
}

func (r *recorder) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if req.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("request without bearer token: %s", req.URL.Path)
		}
		if req.Method == http.MethodGet {
			r.gets = append(r.gets, req.URL.Path)
			r.serveGet(t, w, req)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		r.posts = append(r.posts, body)
		if r.sent != nil {
			select {
			case r.sent <- struct{}{}:
			default:
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if r.status != 0 {
			w.WriteHeader(r.status)
			writeString(t, w, r.response)
			return
		}
		writeString(t, w, `{"messages":[{"id":"wamid.sent"}]}`)
	})
}

func (r *recorder) serveGet(t *testing.T, w http.ResponseWriter, req *http.Request) {
	t.Helper()
	switch {
	case strings.HasPrefix(req.URL.Path, "/dl/"):
		writeString(t, w, "media-bytes")
	case strings.HasSuffix(req.URL.Path, "/MEDIA-BIG"):
		writeString(t, w, `{"url":"http://example.invalid/x","file_size":`+fmt.Sprint(whatsapp_cloud.MaxMediaBytes+1)+`}`)
	default:
		writeString(t, w, `{"url":"http://`+req.Host+`/dl`+req.URL.Path+`","mime_type":"image/jpeg","file_size":11}`)
	}
}

func writeString(t *testing.T, w io.Writer, s string) {
	t.Helper()
	if _, err := io.WriteString(w, s); err != nil {
		t.Errorf("fake graph write: %v", err)
	}
}

func (r *recorder) countPosts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.posts)
}

func (r *recorder) last() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.posts) == 0 {
		return nil
	}
	return r.posts[len(r.posts)-1]
}

func newAdapter(t *testing.T, srv *httptest.Server, h gwplat.Handler, now *time.Time) *whatsapp_cloud.Adapter {
	t.Helper()
	d := descriptor(t)
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
	out, err := gwplat.NewOutbox(&d, sched)
	if err != nil {
		t.Fatal(err)
	}
	a, err := whatsapp_cloud.New(t.Context(), &whatsapp_cloud.Config{
		Enabled:       true,
		PhoneNumberID: testPhone,
		BaseURL:       srv.URL,
		HTTPClient:    srv.Client(),
		Now:           func() time.Time { return *now },
	}, &d, testSecrets(), out, h)
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

// TestF7PLAT026WhatsAppCloud covers the Cloud API surface: explicit
// activation, the GET handshake, deduped inbound dispatch, the 24-hour window
// (a closed window is refused with a reason and zero requests), templates,
// and the media ceiling on both directions.
func TestF7PLAT026WhatsAppCloud(t *testing.T) {
	rec := &recorder{sent: make(chan struct{}, 8)}
	srv := httptest.NewServer(rec.handler(t))
	defer srv.Close()
	now := time.Unix(1_700_000_000, 0)
	var handled []*gateway.Inbound
	h := gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error {
		handled = append(handled, in)
		return nil
	})
	a := newAdapter(t, srv, h, &now)

	t.Run("activation", func(t *testing.T) { cloudActivation(t, a, h) })
	t.Run("handshake", func(t *testing.T) { cloudHandshake(t, a) })
	cloudInbound(t, a, &handled, now)
	t.Run("session_window", func(t *testing.T) { cloudSessionWindow(t, a, rec, &now) })
	t.Run("graph_window_error", func(t *testing.T) { cloudGraphWindow(t, a, rec) })
	t.Run("media", func(t *testing.T) { cloudMedia(t, a, rec, &handled, now) })
	t.Run("outbox", func(t *testing.T) { cloudOutbox(t, a, rec) })
}

func cloudActivation(t *testing.T, a *whatsapp_cloud.Adapter, h gwplat.Handler) {
	t.Helper()
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
}

func cloudHandshake(t *testing.T, a *whatsapp_cloud.Adapter) {
	t.Helper()
	if _, err := a.Verify("subscribe", "wrong", "challenge"); !errors.Is(err, whatsapp_cloud.ErrVerify) {
		t.Fatalf("wrong verify token = %v", err)
	}
	if _, err := a.Verify("unsubscribe", testVerify, "challenge"); !errors.Is(err, whatsapp_cloud.ErrVerify) {
		t.Fatalf("wrong hub.mode = %v", err)
	}
	got, err := a.Verify("subscribe", testVerify, "challenge-123")
	if err != nil || got != "challenge-123" {
		t.Fatalf("handshake = %q/%v", got, err)
	}
}

func cloudInbound(t *testing.T, a *whatsapp_cloud.Adapter, handled *[]*gateway.Inbound, now time.Time) {
	t.Helper()
	body := webhookBody(t, "wamid.1", "628111", "hello", now)
	n, err := a.HandleWebhook(t.Context(), header(sign(body)), body)
	if err != nil || n != 1 || len(*handled) != 1 {
		t.Fatalf("inbound = %d/%v handled=%d", n, err, len(*handled))
	}
	if in := (*handled)[0]; in.Platform != "whatsapp_cloud" || in.ChatID != "628111" || in.Text != "hello" || in.UpdateID != "wamid.1" {
		t.Fatalf("inbound fields = %+v", in)
	}
	n, err = a.HandleWebhook(t.Context(), header(sign(body)), body)
	if err != nil || n != 0 || len(*handled) != 1 {
		t.Fatalf("replay = %d/%v handled=%d", n, err, len(*handled))
	}
}

func cloudSessionWindow(t *testing.T, a *whatsapp_cloud.Adapter, rec *recorder, now *time.Time) {
	t.Helper()
	before := rec.countPosts()
	if err := a.SendSession(t.Context(), "628111", "inside"); err != nil {
		t.Fatal(err)
	}
	if rec.countPosts() != before+1 {
		t.Fatal("session send inside the window wrote no request")
	}
	*now = now.Add(25 * time.Hour)
	closed := a.SendSession(t.Context(), "628111", "outside")
	if !errors.Is(closed, whatsapp_cloud.ErrWindowClosed) {
		t.Fatalf("outside window = %v, want ErrWindowClosed", closed)
	}
	var typed *whatsapp_cloud.WindowClosed
	if !errors.As(closed, &typed) || typed.Reason() != "session_window_closed" {
		t.Fatalf("rejection reason = %v", closed)
	}
	if rec.countPosts() != before+1 {
		t.Fatal("closed window still wrote a request")
	}
	if err := a.SendTemplate(t.Context(), "628111", "hello_world", "en_US", nil); err != nil {
		t.Fatalf("template after window: %v", err)
	}
	if got := rec.last(); got["type"] != "template" {
		t.Fatalf("template payload = %v", got)
	}
	*now = now.Add(-25 * time.Hour)
}

func cloudGraphWindow(t *testing.T, a *whatsapp_cloud.Adapter, rec *recorder) {
	t.Helper()
	before := rec.countPosts()
	rec.status, rec.response = http.StatusBadRequest, `{"error":{"message":"re-engagement","code":131047}}`
	err := a.SendSession(t.Context(), "628111", "late")
	if !errors.Is(err, whatsapp_cloud.ErrWindowClosed) {
		t.Fatalf("131047 = %v, want ErrWindowClosed", err)
	}
	if rec.countPosts() != before+1 {
		t.Fatal("expected exactly one request")
	}
	rec.status, rec.response = 0, ""
}

func cloudMedia(t *testing.T, a *whatsapp_cloud.Adapter, rec *recorder, handled *[]*gateway.Inbound, now time.Time) {
	t.Helper()
	before := rec.countPosts()
	if err := a.SendMedia(t.Context(), &whatsapp_cloud.Media{Chat: "628111", Kind: "image", ID: "m1", Size: 6 << 20}); !errors.Is(err, whatsapp_cloud.ErrMediaTooLarge) {
		t.Fatalf("oversize upload = %v", err)
	}
	if rec.countPosts() != before {
		t.Fatal("oversize upload wrote a request")
	}
	if err := a.SendMedia(t.Context(), &whatsapp_cloud.Media{Chat: "628111", Kind: "image", ID: "m1", Size: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if err := a.SendMedia(t.Context(), &whatsapp_cloud.Media{Chat: "628111", Kind: "application", ID: "m1"}); !errors.Is(err, whatsapp_cloud.ErrMediaTooLarge) {
		t.Fatalf("unknown media kind = %v", err)
	}
	oversize := strings.Replace(string(webhookBody(t, "wamid.media", "628111", "pic", now)),
		`"type":"text"`, `"type":"image","image":{"id":"m2","mime_type":"image/jpeg","file_size":6291456}`, 1)
	*handled = (*handled)[:0]
	if _, err := a.HandleWebhook(t.Context(), header(sign([]byte(oversize))), []byte(oversize)); !errors.Is(err, whatsapp_cloud.ErrMediaTooLarge) {
		t.Fatalf("oversize inbound = %v, want ErrMediaTooLarge", err)
	}
	if len(*handled) != 0 {
		t.Fatalf("oversize inbound dispatched %d messages", len(*handled))
	}
	if _, _, err := a.DownloadMedia(t.Context(), "MEDIA-BIG"); !errors.Is(err, whatsapp_cloud.ErrMediaTooLarge) {
		t.Fatalf("oversize download = %v", err)
	}
	data, mime, err := a.DownloadMedia(t.Context(), "m3")
	if err != nil || string(data) != "media-bytes" || mime != "image/jpeg" {
		t.Fatalf("download = %q/%q/%v", data, mime, err)
	}
}

func cloudOutbox(t *testing.T, a *whatsapp_cloud.Adapter, rec *recorder) {
	t.Helper()
	if err := a.Reply(t.Context(), gwplat.Reply{Chat: "628111", Markdown: "hello **world**"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-rec.sent:
	case <-time.After(5 * time.Second):
		t.Fatal("outbox never sent the reply")
	}
	payload, err := json.Marshal(rec.last())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "**") {
		t.Fatalf("markdown leaked to the WhatsApp wire: %s", payload)
	}
}

// TestF7WACloudSignature is the negative control of the webhook boundary: a
// bad or missing X-Hub-Signature-256 admits zero messages, a tampered body is
// rejected, a valid signature admits exactly one, and the secret never leaks
// into the error text.
func TestF7WACloudSignature(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	defer srv.Close()
	now := time.Unix(1_700_000_000, 0)
	var handled int
	h := gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error {
		handled++
		return nil
	})
	a := newAdapter(t, srv, h, &now)
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
	if handled != 0 {
		t.Fatalf("unsigned webhook dispatched %d messages", handled)
	}
	if n, err := a.HandleWebhook(t.Context(), header(sign(body)), body); err != nil || n != 1 || handled != 1 {
		t.Fatalf("signed webhook = %d/%v handled=%d", n, err, handled)
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

// TestWhatsAppCloudInboundAdmission proves explicit activation, the GET
// handshake, and the inbound ceilings that hold before any request is written.
func TestWhatsAppCloudInboundAdmission(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	defer srv.Close()
	now := time.Unix(1_700_000_000, 0)
	h := gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil })
	a := newAdapter(t, srv, h, &now)
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
	if _, err := whatsapp_cloud.New(t.Context(), &whatsapp_cloud.Config{Enabled: true, PhoneNumberID: testPhone, BaseURL: "http://example.com"}, &d, testSecrets(), a.Outbox(), h); !errors.Is(err, whatsapp_cloud.ErrConfig) {
		t.Fatalf("non-loopback plaintext base URL = %v, want ErrConfig", err)
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
	big := make([]byte, whatsapp_cloud.MaxWebhookBytes+1)
	if _, err := a.HandleWebhook(t.Context(), header(sign(big)), big); !errors.Is(err, whatsapp_cloud.ErrWebhookTooBig) {
		t.Fatalf("oversize body = %v, want ErrWebhookTooBig", err)
	}
}
