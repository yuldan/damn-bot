package wecom

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

const encodingKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"

func encrypt(t *testing.T, msg []byte) string {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(encodingKey + "=")
	if err != nil || len(key) != 32 {
		t.Fatal("bad key")
	}
	plain := make([]byte, 16, 20+len(msg)+16)
	if _, rerr := rand.Read(plain); rerr != nil {
		t.Fatal(rerr)
	}
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(msg))) // #nosec G115 -- fixture is tiny.
	plain = append(plain, n[:]...)
	plain = append(plain, msg...)
	plain = append(plain, []byte("corp")...)
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, key[:aes.BlockSize]).CryptBlocks(out, plain)
	return base64.StdEncoding.EncodeToString(out)
}

func TestF7PLAT023WeCom(t *testing.T) {
	msg := `<xml><FromUserName><![CDATA[u-1]]></FromUserName><MsgType>text</MsgType><Content>halo</Content><MsgId>m-1</MsgId></xml>`
	enc := encrypt(t, []byte(msg))
	payload := `<xml><Encrypt><![CDATA[` + enc + `]]></Encrypt></xml>`
	ts, nonce := "1700000000", "n1"
	sig := signature("token", ts, nonce, enc)
	target := "/wecom?" + url.Values{"msg_signature": {sig}, "timestamp": {ts}, "nonce": {nonce}}.Encode()
	reg, rerr := gwplat.NewDefaultRegistry()
	if rerr != nil {
		t.Fatal(rerr)
	}
	desc, derr := reg.Lookup("wecom")
	if derr != nil {
		t.Fatal(derr)
	}
	got := 0
	a, err := New(t.Context(), &Config{Enabled: true}, &desc,
		secrets{"wecom_token": "token", "wecom_encoding_aes_key": encodingKey, "wecom_corp_id": "corp", "wecom_corp_secret": "s"},
		newOutbox(t), gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { got++; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	for range 2 {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://example.com"+target, strings.NewReader(payload))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("valid = %d", rec.Code)
		}
	}
	if got != 1 {
		t.Fatalf("turns = %d", got)
	}
	bad := "/wecom?" + url.Values{"msg_signature": {"bad"}, "timestamp": {ts}, "nonce": {nonce}}.Encode()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://example.com"+bad, strings.NewReader(payload))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || got != 1 {
		t.Fatalf("bad signature status=%d turns=%d", rec.Code, got)
	}

	wecomWS(t, &desc)
}

var _ = json.Marshal

func wecomWS(t *testing.T, desc *gwplat.Descriptor) {
	// Smart Robot websocket surface.
	ch := make(chan gateway.Inbound, 2)
	wsSrv := wsServer(t, func(ctx context.Context, ws *websocket.Conn) {
		frame := []byte(`{"msgid":"ws-1","msgtype":"text","text":{"content":"ws"},"from":{"userid":"u-1"},"chatid":"c-1"}`)
		if werr := ws.Write(ctx, websocket.MessageText, frame); werr != nil {
			return
		}
		if _, _, rerr := ws.Read(ctx); rerr != nil {
			return
		}
		if werr := ws.Write(ctx, websocket.MessageText, frame); werr != nil {
			return
		}
		if _, _, rerr := ws.Read(ctx); rerr != nil {
			return
		}
		<-ctx.Done()
	})
	defer wsSrv.Close()
	wa, err := New(t.Context(), &Config{Enabled: true, WSURL: "ws" + strings.TrimPrefix(wsSrv.URL, "http")}, desc,
		secrets{"wecom_token": "token", "wecom_encoding_aes_key": encodingKey, "wecom_corp_id": "corp", "wecom_corp_secret": "s"},
		newOutbox(t), gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error { ch <- *in; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		if err := wa.Run(ctx); err != nil {
			return
		}
	}()
	in := awaitInbound(t, ch)
	if in.UpdateID != "ws-1" {
		t.Fatalf("ws inbound = %+v", in)
	}
	awaitNoInbound(t, ch)
}

func TestF7WeComXML(t *testing.T) {
	reg, rerr := gwplat.NewDefaultRegistry()
	if rerr != nil {
		t.Fatal(rerr)
	}
	desc, derr := reg.Lookup("wecom")
	if derr != nil {
		t.Fatal(derr)
	}
	got := 0
	a, err := New(t.Context(), &Config{Enabled: true}, &desc,
		secrets{"wecom_token": "token", "wecom_encoding_aes_key": encodingKey, "wecom_corp_id": "corp", "wecom_corp_secret": "s"},
		newOutbox(t), gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { got++; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	for _, evil := range []string{
		`<?xml version="1.0"?><!DOCTYPE x [<!ENTITY e SYSTEM "file:///etc/passwd">]><xml><Encrypt>&e;</Encrypt></xml>`,
		`<xml><Encrypt><![CDATA[x]]></Encrypt></xml><!ENTITY x SYSTEM "file:///etc/passwd">`,
	} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/wecom?msg_signature=x&timestamp=1&nonce=2", strings.NewReader(evil))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("external entity = %d", rec.Code)
		}
	}
	if got != 0 {
		t.Fatal("malicious XML ran a turn")
	}
}

func wsServer(t *testing.T, fn func(context.Context, *websocket.Conn)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() {
			if err := ws.CloseNow(); err != nil {
				return
			}
		}()
		fn(r.Context(), ws)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWeComSend(t *testing.T) {
	sent := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			if _, err := w.Write([]byte(`{"access_token":"wtok","expires_in":7200}`)); err != nil {
				return
			}
		case "/cgi-bin/message/send":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return
			}
			sent <- body
			if _, err := w.Write([]byte(`{"errcode":0}`)); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup("wecom")
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(t.Context(), &Config{Enabled: true, API: srv.URL}, &desc,
		secrets{"wecom_token": "token", "wecom_encoding_aes_key": encodingKey, "wecom_corp_id": "corp", "wecom_corp_secret": "s"},
		newOutbox(t), gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Send(t.Context(), "u-1", "**halo**"); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-sent:
		if !strings.Contains(string(mustJSON(t, body)), "halo") {
			t.Fatalf("sent = %v", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no wecom send")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
