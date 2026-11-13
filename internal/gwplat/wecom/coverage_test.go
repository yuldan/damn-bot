package wecom

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/outq"
	"github.com/damnbot/damnbot/internal/gwplat"
)

func wecomSecrets() secrets {
	return secrets{
		"wecom_token": "token", "wecom_encoding_aes_key": encodingKey,
		"wecom_corp_id": "corp", "wecom_corp_secret": "corp-secret",
	}
}

func okHandler() gwplat.Handler {
	return gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil })
}

func writeBody(w http.ResponseWriter, body string) {
	if _, err := w.Write([]byte(body)); err != nil {
		return
	}
}

func newWecom(t *testing.T, cfg Config, h gwplat.Handler) *Adapter {
	t.Helper()
	return newWecomWithOutbox(t, cfg, newOutbox(t), h)
}

func newWecomWithOutbox(t *testing.T, cfg Config, out *gwplat.Outbox, h gwplat.Handler) *Adapter {
	t.Helper()
	desc, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	d, err := desc.Lookup("wecom")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	cfg.Enabled = true
	a, err := New(t.Context(), &cfg, &d, wecomSecrets(), out, h)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func wecomErrorOutbox(t *testing.T) (*gwplat.Outbox, <-chan error) {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	desc, err := reg.Lookup("wecom")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	errs := make(chan error, 8)
	sched, err := outq.New(&outq.Options{
		Pacer: allowPacer{}, MaxAttempts: 1,
		OnError: func(_ outq.Message, sendErr error) { errs <- sendErr },
	})
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}
	out, err := gwplat.NewOutbox(&desc, sched)
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		if rerr := sched.Run(ctx); rerr != nil {
			return
		}
	}()
	return out, errs
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	desc := mustDesc(t, "wecom")
	other := mustDesc(t, "dingtalk")
	out := newOutbox(t)
	h := okHandler()
	on := &Config{Enabled: true}
	cases := []struct {
		name string
		cfg  *Config
		desc *gwplat.Descriptor
		out  *gwplat.Outbox
		h    gwplat.Handler
		want error
	}{
		{name: "nil descriptor", cfg: on, out: out, h: h, want: ErrConfig},
		{name: "wrong descriptor", cfg: on, desc: &other, out: out, h: h, want: ErrConfig},
		{name: "nil config", desc: &desc, out: out, h: h, want: ErrConfig},
		{name: "disabled", cfg: &Config{}, desc: &desc, out: out, h: h, want: gwplat.ErrUnavailable},
		{name: "nil outbox", cfg: on, desc: &desc, h: h, want: ErrConfig},
		{name: "nil handler", cfg: on, desc: &desc, out: out, want: ErrConfig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(t.Context(), tc.cfg, tc.desc, wecomSecrets(), tc.out, tc.h); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNewRequiresSecrets(t *testing.T) {
	desc := mustDesc(t, "wecom")
	cases := []struct {
		name string
		src  gwplat.SecretSource
	}{
		{name: "nil source"},
		{name: "missing key", src: secrets{"wecom_token": "t"}},
		{name: "blank value", src: secrets{
			"wecom_token": "t", "wecom_encoding_aes_key": " ",
			"wecom_corp_id": "c", "wecom_corp_secret": "s",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(t.Context(), &Config{Enabled: true}, &desc, tc.src, newOutbox(t), okHandler())
			if !errors.Is(err, gwplat.ErrMissingSecret) {
				t.Fatalf("err = %v, want ErrMissingSecret", err)
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	a := newWecom(t, Config{API: "http://example.test/"}, okHandler())
	if a.api != "http://example.test" {
		t.Fatalf("api = %q, want trailing slash trimmed", a.api)
	}
	if a.now == nil || a.http.Client == nil {
		t.Fatal("nil default seam")
	}
	fixed := time.Unix(1700000000, 0)
	b := newWecom(t, Config{Now: func() time.Time { return fixed }, WSURL: "ws://example.test"}, okHandler())
	if !b.now().Equal(fixed) || b.wsURL != "ws://example.test" {
		t.Fatalf("now/ws = %s/%q", b.now(), b.wsURL)
	}
}

func tokenAdapter(t *testing.T, body string) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBody(w, body)
	}))
	t.Cleanup(srv.Close)
	return newWecom(t, Config{API: srv.URL}, okHandler())
}

func assertBadToken(t *testing.T, a *Adapter) {
	t.Helper()
	if _, err := a.accessToken(t.Context()); !errors.Is(err, gwplat.ErrBadPayload) {
		t.Fatalf("err = %v, want ErrBadPayload", err)
	}
}

func TestAccessTokenFailures(t *testing.T) {
	t.Run("bad json", func(t *testing.T) {
		assertBadToken(t, tokenAdapter(t, `{"access_token":`))
	})
	t.Run("empty token", func(t *testing.T) {
		assertBadToken(t, tokenAdapter(t, `{"access_token":"","expires_in":7200}`))
	})
	t.Run("status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", http.StatusBadGateway)
		}))
		t.Cleanup(srv.Close)
		a := newWecom(t, Config{API: srv.URL}, okHandler())
		var se *gwplat.StatusError
		if _, err := a.accessToken(t.Context()); !errors.As(err, &se) || se.Code != http.StatusBadGateway {
			t.Fatalf("err = %v, want status 502", err)
		}
	})
	t.Run("transport", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := srv.URL
		srv.Close()
		a := newWecom(t, Config{API: url}, okHandler())
		if _, err := a.accessToken(t.Context()); err == nil {
			t.Fatal("closed server must fail")
		}
	})
}

func TestAccessTokenCacheAndQuery(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if q.Get("corpid") != "corp" || q.Get("corpsecret") != "corp-secret" {
			http.Error(w, "credentials", http.StatusUnauthorized)
			return
		}
		writeBody(w, `{"access_token":"tok","expires_in":7200}`)
	}))
	t.Cleanup(srv.Close)
	now := time.Unix(1700000000, 0)
	a := newWecom(t, Config{API: srv.URL, Now: func() time.Time { return now }}, okHandler())
	for range 2 {
		tok, err := a.accessToken(t.Context())
		if err != nil || tok != "tok" {
			t.Fatalf("token = %q, %v", tok, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("token calls = %d, want 1 (cached)", got)
	}
	now = now.Add(3 * time.Hour)
	if _, err := a.accessToken(t.Context()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("token calls = %d, want 2 after expiry", got)
	}
}

func TestSendFailures(t *testing.T) {
	t.Run("token", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		out, errs := wecomErrorOutbox(t)
		a := newWecomWithOutbox(t, Config{API: srv.URL}, out, okHandler())
		if err := a.Send(t.Context(), "u", "x"); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if err := awaitSendError(t, errs); err == nil {
			t.Fatal("token failure was not reported")
		}
	})
	t.Run("send status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/gettoken") {
				writeBody(w, `{"access_token":"tok","expires_in":7200}`)
				return
			}
			if r.URL.Query().Get("access_token") != "tok" {
				http.Error(w, "token query", http.StatusUnauthorized)
				return
			}
			http.Error(w, "nope", http.StatusBadGateway)
		}))
		t.Cleanup(srv.Close)
		out, errs := wecomErrorOutbox(t)
		a := newWecomWithOutbox(t, Config{API: srv.URL}, out, okHandler())
		if err := a.Send(t.Context(), "u", "x"); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		var se *gwplat.StatusError
		if err := awaitSendError(t, errs); !errors.As(err, &se) || se.Code != http.StatusBadGateway {
			t.Fatalf("err = %v, want status 502", err)
		}
	})
	t.Run("empty chat", func(t *testing.T) {
		a := newWecom(t, Config{}, okHandler())
		if err := a.Send(t.Context(), "", "x"); !errors.Is(err, gwplat.ErrNoReply) {
			t.Fatalf("err = %v, want ErrNoReply", err)
		}
	})
}

func TestSendBody(t *testing.T) {
	sent := make(chan map[string]any, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/gettoken") {
			writeBody(w, `{"access_token":"tok","expires_in":7200}`)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return
		}
		sent <- body
		writeBody(w, `{"errcode":0}`)
	}))
	t.Cleanup(srv.Close)
	a := newWecom(t, Config{API: srv.URL}, okHandler())
	if err := a.Send(t.Context(), "u-1", "halo"); err != nil {
		t.Fatal(err)
	}
	body := awaitValue(t, sent)
	if body["touser"] != "u-1" || body["msgtype"] != "text" {
		t.Fatalf("body = %v", body)
	}
	if !strings.Contains(string(mustJSON(t, body)), "halo") {
		t.Fatalf("body = %v", body)
	}
}

func TestSendChunksAtSizeLimit(t *testing.T) {
	sent := make(chan map[string]any, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/gettoken") {
			writeBody(w, `{"access_token":"tok","expires_in":7200}`)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return
		}
		sent <- body
		writeBody(w, `{"errcode":0}`)
	}))
	t.Cleanup(srv.Close)
	a := newWecom(t, Config{API: srv.URL}, okHandler())
	markdown := strings.Repeat("a", 9000)
	if err := a.Send(t.Context(), "u", markdown); err != nil {
		t.Fatal(err)
	}
	total := 0
	for range 3 {
		body := awaitValue(t, sent)
		text, ok := body["text"].(map[string]any)
		if !ok {
			t.Fatalf("text = %v", body["text"])
		}
		content, ok := text["content"].(string)
		if !ok || len(content) > gateway.DefaultMaxLen {
			t.Fatalf("content = %v", text["content"])
		}
		total += len(content)
	}
	if total < len(markdown) {
		t.Fatalf("chunks cover %d < %d", total, len(markdown))
	}
}

func awaitSendError(t *testing.T, errs <-chan error) error {
	t.Helper()
	select {
	case err := <-errs:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("send failure was never reported")
		return nil
	}
}

func mustDesc(t *testing.T, name string) gwplat.Descriptor {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	d, err := reg.Lookup(name)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	return d
}

func awaitValue[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("no value")
		var zero T
		return zero
	}
}

func callbackRequest(t *testing.T, enc, sig string) *http.Request {
	t.Helper()
	q := url.Values{"msg_signature": {sig}, "timestamp": {"1700000000"}, "nonce": {"n"}}
	body := `<xml><Encrypt><![CDATA[` + enc + `]]></Encrypt></xml>`
	return httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/wecom?"+q.Encode(), strings.NewReader(body))
}

func callbackSignature(enc string) string { return signature("token", "1700000000", "n", enc) }

func textPayload(text string) string {
	return `<xml><FromUserName><![CDATA[u-1]]></FromUserName><MsgType>text</MsgType><Content>` +
		text + `</Content><MsgId>m-1</MsgId></xml>`
}

func TestHandlerRejectsBadRequests(t *testing.T) {
	enc := encrypt(t, []byte(textPayload("hi")))
	h := newWecom(t, Config{}, okHandler()).Handler()
	cases := []struct {
		name     string
		method   string
		body     string
		envelope string
		sig      string
		want     int
	}{
		{name: "method", method: http.MethodPut, body: `<xml/>`, sig: callbackSignature(enc), want: http.StatusMethodNotAllowed},
		{name: "bad xml", method: http.MethodPost, body: `<xml`, sig: callbackSignature(enc), want: http.StatusBadRequest},
		{name: "bad signature", method: http.MethodPost, sig: "deadbeef", want: http.StatusUnauthorized},
		{name: "undecryptable", method: http.MethodPost, envelope: "zzz", sig: callbackSignature("zzz"), want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req *http.Request
			if tc.body == "" {
				envelope := tc.envelope
				if envelope == "" {
					envelope = enc
				}
				req = callbackRequest(t, envelope, tc.sig)
			} else {
				req = httptest.NewRequestWithContext(t.Context(), tc.method, "/wecom?msg_signature="+tc.sig,
					strings.NewReader(tc.body))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestHandlerRejectsNonTextPayloads(t *testing.T) {
	for name, plain := range map[string]string{
		"event":      `<xml><FromUserName>u</FromUserName><MsgType>event</MsgType><MsgId>m</MsgId></xml>`,
		"missing id": `<xml><FromUserName>u</FromUserName><MsgType>text</MsgType><Content>hi</Content></xml>`,
	} {
		t.Run(name, func(t *testing.T) {
			enc := encrypt(t, []byte(plain))
			h := newWecom(t, Config{}, okHandler()).Handler()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, callbackRequest(t, enc, callbackSignature(enc)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestHandlerDispatchError(t *testing.T) {
	enc := encrypt(t, []byte(textPayload("hi")))
	h := newWecom(t, Config{}, gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error {
		return errors.New("boom")
	})).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, callbackRequest(t, enc, callbackSignature(enc)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestHandlerDedupesReplay(t *testing.T) {
	enc := encrypt(t, []byte(textPayload("hi")))
	var calls atomic.Int32
	h := newWecom(t, Config{}, gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error {
		calls.Add(1)
		return nil
	})).Handler()
	for range 3 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, callbackRequest(t, enc, callbackSignature(enc)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("turns = %d, want 1", got)
	}
}

func TestHandlerBodyLimit(t *testing.T) {
	enc := encrypt(t, []byte(textPayload("hi")))
	a := newWecom(t, Config{}, okHandler())
	a.http.MaxBody = 32
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, callbackRequest(t, enc, callbackSignature(enc)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestVerifyURL(t *testing.T) {
	echo := encrypt(t, []byte("echo-123"))
	sig := signature("token", "1700000000", "n", echo)
	a := newWecom(t, Config{}, okHandler())
	cases := []struct {
		name    string
		echostr string
		sig     string
		want    int
	}{
		{name: "valid", echostr: echo, sig: sig, want: http.StatusOK},
		{name: "bad signature", echostr: echo, sig: "deadbeef", want: http.StatusUnauthorized},
		{name: "bad ciphertext", echostr: "!!!", sig: signature("token", "1700000000", "n", "!!!"), want: http.StatusUnauthorized},
		{name: "missing params", sig: "", want: http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := url.Values{
				"msg_signature": {tc.sig}, "timestamp": {"1700000000"}, "nonce": {"n"},
				"echostr": {tc.echostr},
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/wecom?"+q.Encode(), nil)
			rec := httptest.NewRecorder()
			a.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if tc.name == "valid" && rec.Body.String() != "echo-123" {
				t.Fatalf("echo = %q", rec.Body.String())
			}
		})
	}
}

func encryptBlocks(t *testing.T, plain []byte) string {
	t.Helper()
	if len(plain)%aes.BlockSize != 0 {
		t.Fatalf("plain = %d bytes, not block aligned", len(plain))
	}
	key, err := base64.StdEncoding.DecodeString(encodingKey + "=")
	if err != nil || len(key) != 32 {
		t.Fatalf("key: %v", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, key[:aes.BlockSize]).CryptBlocks(out, plain)
	return base64.StdEncoding.EncodeToString(out)
}

func TestDecryptRejectsMalformedCiphertext(t *testing.T) {
	zeroPad := make([]byte, 32)
	bigPad := make([]byte, 32)
	bigPad[31] = 17
	mismatch := make([]byte, 32)
	mismatch[31] = 5
	shortPlain := make([]byte, 16)
	shortPlain[15] = 1
	badLen := make([]byte, 32)
	badLen[15] = 1
	copy(badLen[16:20], []byte{0xff, 0xff, 0xff, 0xff})
	cases := []struct {
		name      string
		key       string
		encrypted string
	}{
		{name: "bad key base64", key: "!!!", encrypted: encryptBlocks(t, zeroPad)},
		{name: "short key", key: base64.StdEncoding.EncodeToString(make([]byte, 16)), encrypted: encryptBlocks(t, zeroPad)},
		{name: "bad ciphertext base64", key: encodingKey, encrypted: "!!!"},
		{name: "short ciphertext", key: encodingKey, encrypted: base64.StdEncoding.EncodeToString(make([]byte, 16))},
		{name: "unaligned ciphertext", key: encodingKey, encrypted: base64.StdEncoding.EncodeToString(make([]byte, 17))},
		{name: "zero padding", key: encodingKey, encrypted: encryptBlocks(t, zeroPad)},
		{name: "oversized padding", key: encodingKey, encrypted: encryptBlocks(t, bigPad)},
		{name: "inconsistent padding", key: encodingKey, encrypted: encryptBlocks(t, mismatch)},
		{name: "short plaintext", key: encodingKey, encrypted: encryptBlocks(t, shortPlain)},
		{name: "length overflow", key: encodingKey, encrypted: encryptBlocks(t, badLen)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decrypt(tc.key, tc.encrypted); !errors.Is(err, gwplat.ErrBadSignature) {
				t.Fatalf("err = %v, want ErrBadSignature", err)
			}
		})
	}
}

func TestDecryptRoundTrip(t *testing.T) {
	plain := []byte(textPayload("halo"))
	got, err := decrypt(encodingKey, encrypt(t, plain))
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("plain = %q", got)
	}
}

func TestParseXMLAndMessage(t *testing.T) {
	var v struct {
		MsgType string `xml:"MsgType"`
	}
	if err := parseXML([]byte(`<xml><MsgType>text</MsgType></xml>`), &v); err != nil || v.MsgType != "text" {
		t.Fatalf("xml = %+v, %v", v, err)
	}
	if err := parseXML([]byte(`<xml><a></xml>`), &v); !errors.Is(err, gwplat.ErrBadPayload) {
		t.Fatalf("err = %v, want ErrBadPayload", err)
	}
	for _, evil := range []string{"<!DOCTYPE", "<!ENTITY", `SYSTEM "f"`, "public"} {
		if err := parseXML([]byte(`<xml>`+evil+`</xml>`), &v); !errors.Is(err, ErrExternalEntity) {
			t.Fatalf("%q: err = %v, want ErrExternalEntity", evil, err)
		}
	}
	in, ok := parseMessage([]byte(textPayload("halo")))
	if !ok || in.Platform != "wecom" || in.ChatID != "u-1" || in.SenderID != "u-1" ||
		in.Text != "halo" || in.UpdateID != "m-1" || !in.Mentioned {
		t.Fatalf("inbound = %+v ok=%v", in, ok)
	}
	if _, ok := parseMessage([]byte(`<xml><MsgType>event</MsgType><MsgId>m</MsgId></xml>`)); ok {
		t.Fatal("event accepted as text")
	}
}

func TestSignatureIsCanonical(t *testing.T) {
	first := signature("t", "1", "n", "e")
	second := signature("e", "n", "1", "t")
	if first != second || len(first) != 40 {
		t.Fatalf("signature = %q vs %q", first, second)
	}
	if signature("other", "1", "n", "e") == first {
		t.Fatal("token did not change signature")
	}
}

func TestRunRequiresWSURL(t *testing.T) {
	a := newWecom(t, Config{}, okHandler())
	if err := a.Run(t.Context()); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestRunCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	a := newWecom(t, Config{WSURL: "ws://127.0.0.1:1/x"}, okHandler())
	if err := a.Run(ctx); err == nil {
		t.Fatal("cancelled run returned nil")
	}
}

func TestRunReconnectsThenCancel(t *testing.T) {
	var attempts atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 3 {
			cancel()
		}
		http.Error(w, "no upgrade", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	a := newWecom(t, Config{
		WSURL:   "ws" + strings.TrimPrefix(srv.URL, "http"),
		Backoff: gwplat.Backoff{Base: time.Millisecond, Max: time.Millisecond},
	}, okHandler())
	errCh := make(chan error, 1)
	go func() { errCh <- a.Run(ctx) }()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("cancelled run returned nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop")
	}
	if got := attempts.Load(); got < 2 {
		t.Fatalf("ws attempts = %d, want retry", got)
	}
}

func streamFrames(ctx context.Context, ws *websocket.Conn, valid string, acked chan<- struct{}) {
	frames := []string{"nope", `{"msgid":"","chatid":"c"}`, valid, valid}
	for i, frame := range frames {
		if err := ws.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
			return
		}
		if i < 2 {
			continue
		}
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
	}
	close(acked)
	<-ctx.Done()
}

func TestRunSkipsBadFramesAndDedupes(t *testing.T) {
	const valid = `{"msgid":"m-1","text":{"content":"hi"},"from":{"userid":"u-1"},"chatid":"c-1"}`
	acked := make(chan struct{})
	srv := wsServer(t, func(ctx context.Context, ws *websocket.Conn) {
		streamFrames(ctx, ws, valid, acked)
	})
	defer srv.Close()
	got := make(chan gateway.Inbound, 2)
	a := newWecom(t, Config{
		WSURL:   "ws" + strings.TrimPrefix(srv.URL, "http"),
		Backoff: gwplat.Backoff{Base: time.Millisecond, Max: time.Millisecond},
	},
		gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error { got <- *in; return nil }))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- a.Run(ctx) }()
	select {
	case in := <-got:
		if in.Platform != "wecom" || in.ChatID != "c-1" || in.SenderID != "u-1" ||
			in.UpdateID != "m-1" || in.Text != "hi" || !in.Group || !in.Mentioned {
			t.Fatalf("inbound = %+v", in)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no dispatch")
	}
	select {
	case <-acked:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not see every ack")
	}
	awaitNoInbound(t, got)
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("cancelled run returned nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop")
	}
}

func TestSessionDispatchError(t *testing.T) {
	frame := []byte(`{"msgid":"m-1","chatid":"c-1","text":{"content":"hi"},"from":{"userid":"u-1"}}`)
	srv := wsServer(t, func(ctx context.Context, ws *websocket.Conn) {
		if err := ws.Write(ctx, websocket.MessageText, frame); err != nil {
			return
		}
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
		<-ctx.Done()
	})
	defer srv.Close()
	a := newWecom(t, Config{WSURL: "ws" + strings.TrimPrefix(srv.URL, "http")},
		gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return errors.New("boom") }))
	if err := a.session(t.Context()); err == nil || !strings.Contains(err.Error(), "dispatch") {
		t.Fatalf("err = %v, want dispatch error", err)
	}
}

func TestSessionDialFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no upgrade", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	a := newWecom(t, Config{WSURL: "ws" + strings.TrimPrefix(srv.URL, "http")}, okHandler())
	if err := a.session(t.Context()); err == nil {
		t.Fatal("dial failure must surface")
	}
}

type failingCloser struct{}

func (failingCloser) Read([]byte) (int, error) { return 0, io.EOF }
func (failingCloser) Close() error             { return errors.New("close failure") }

func TestCloseDialBody(t *testing.T) {
	closeDialBody(nil)
	closeDialBody(&http.Response{})
	closeDialBody(&http.Response{Body: failingCloser{}})
}
