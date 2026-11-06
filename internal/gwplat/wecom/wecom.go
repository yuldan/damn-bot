// Package wecom is the WeCom Smart Robot + callback adapter of F7 PLAT-023.
package wecom

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1" // #nosec G505 -- WeCom signs with SHA1; protocol requirement.
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

// ErrConfig rejects invalid activation data.
var ErrConfig = errors.New("wecom: invalid config")

// ErrExternalEntity rejects DTD/entity declarations.
var ErrExternalEntity = errors.New("wecom: xml external entity rejected")

// Config is explicit activation; the zero value is disabled.
type Config struct {
	Enabled    bool
	API        string
	WSURL      string
	HTTPClient *http.Client
	Now        func() time.Time
	Backoff    gwplat.Backoff
}

// Adapter is one WeCom tenant.
type Adapter struct {
	desc     *gwplat.Descriptor
	out      *gwplat.Outbox
	handler  gwplat.Handler
	token    string
	encKey   string
	corpID   string
	corpAuth string
	api      string
	wsURL    string
	dedupe   *gwplat.Dedupe
	now      func() time.Time
	http     gwplat.HTTP
	backoff  gwplat.Backoff
	mu       sync.Mutex
	access   string
	expires  time.Time
}

// New validates explicit config and secrets; it opens no connection.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource,
	out *gwplat.Outbox, h gwplat.Handler,
) (*Adapter, error) {
	if desc == nil || desc.Name != "wecom" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("wecom: activation: %w", err)
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("wecom: secret: %w", err)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	client := cfg.HTTPClient
	if client == nil {
		client = netx.LoopbackClient()
	}
	api := cfg.API
	if api == "" {
		api = "https://qyapi.weixin.qq.com"
	}
	return &Adapter{
		desc: desc, out: out, handler: h, token: values["wecom_token"],
		encKey: values["wecom_encoding_aes_key"], corpID: values["wecom_corp_id"],
		corpAuth: values["wecom_corp_secret"], api: strings.TrimRight(api, "/"), wsURL: cfg.WSURL,
		dedupe: gwplat.NewDedupe(4096, 30*time.Minute), now: now,
		http: gwplat.HTTP{Client: client}, backoff: cfg.Backoff,
	}, nil
}

func (w *Adapter) accessToken(ctx context.Context) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.access != "" && w.now().Before(w.expires.Add(-time.Minute)) {
		return w.access, nil
	}
	q := url.Values{"corpid": {w.corpID}, "corpsecret": {w.corpAuth}}
	data, err := w.http.Do(ctx, http.MethodGet, w.api+"/cgi-bin/gettoken?"+q.Encode(), nil, nil)
	if err != nil {
		return "", fmt.Errorf("wecom: %w", err)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if json.Unmarshal(data, &out) != nil || out.AccessToken == "" {
		return "", fmt.Errorf("%w: wecom token", gwplat.ErrBadPayload)
	}
	w.access, w.expires = out.AccessToken, w.now().Add(time.Duration(out.ExpiresIn)*time.Second)
	return w.access, nil
}

// Send posts one rendered reply.
func (w *Adapter) Send(ctx context.Context, chat, markdown string) error {
	err := w.out.Reply(ctx, gwplat.Reply{
		Chat: chat, Markdown: markdown,
		Send: func(ctx context.Context, part string) error {
			tok, err := w.accessToken(ctx)
			if err != nil {
				return fmt.Errorf("wecom: %w", err)
			}
			body := map[string]any{"touser": chat, "msgtype": "text", "text": map[string]string{"content": part}}
			_, err = w.http.DoJSON(ctx, w.api+"/cgi-bin/message/send?access_token="+tok, body, nil)
			if err != nil {
				return fmt.Errorf("wecom: %w", err)
			}
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("wecom: outbox: %w", err)
	}
	return nil
}

// Handler is the encrypted callback surface.
func (w *Adapter) Handler() http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.verifyURL(rw, r)
			return
		}
		body, err := w.http.ReadBody(rw, r)
		if err != nil {
			return
		}
		q := r.URL.Query()
		var outer struct {
			Encrypt string `xml:"Encrypt"`
		}
		if parseXML(body, &outer) != nil {
			gwplat.Deny(rw, http.StatusBadRequest)
			return
		}
		if !gwplat.ConstantTimeEqual(signature(w.token, q.Get("timestamp"), q.Get("nonce"), outer.Encrypt),
			q.Get("msg_signature")) {
			gwplat.Deny(rw, http.StatusUnauthorized)
			return
		}
		plain, err := decrypt(w.encKey, outer.Encrypt)
		if err != nil {
			gwplat.Deny(rw, http.StatusBadRequest)
			return
		}
		in, ok := parseMessage(plain)
		if !ok {
			gwplat.Deny(rw, http.StatusBadRequest)
			return
		}
		if !w.dedupe.Claim("wecom:"+in.UpdateID, w.now()) {
			rw.WriteHeader(http.StatusOK)
			return
		}
		if derr := gwplat.Dispatch(r.Context(), w.handler, &in); derr != nil {
			gwplat.Deny(rw, http.StatusInternalServerError)
			return
		}
		rw.WriteHeader(http.StatusOK)
	})
}

func (w *Adapter) verifyURL(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	plain, err := decrypt(w.encKey, q.Get("echostr"))
	if err != nil || !gwplat.ConstantTimeEqual(signature(w.token, q.Get("timestamp"), q.Get("nonce"), q.Get("echostr")),
		q.Get("msg_signature")) {
		gwplat.Deny(rw, http.StatusUnauthorized)
		return
	}
	if _, werr := rw.Write(plain); werr != nil {
		return
	}
}

// Run is the Smart Robot websocket surface.
func (w *Adapter) Run(ctx context.Context) error {
	if w.wsURL == "" {
		return fmt.Errorf("%w: ws url", ErrConfig)
	}
	for attempt := 1; ; attempt++ {
		err := w.session(ctx)
		if ctx.Err() != nil {
			if err != nil {
				return fmt.Errorf("wecom: %w", err)
			}
			return nil
		}
		if waitErr := w.backoff.Wait(ctx, attempt); waitErr != nil {
			return fmt.Errorf("wecom: reconnect: %w", waitErr)
		}
	}
}

func (w *Adapter) session(ctx context.Context) error {
	ws, resp, derr := websocket.Dial(ctx, w.wsURL, nil)
	closeDialBody(resp)
	if derr != nil {
		return fmt.Errorf("wecom: ws: %w", derr)
	}
	defer closeWS(ws)
	for {
		_, raw, rerr := ws.Read(ctx)
		if rerr != nil {
			return fmt.Errorf("wecom: read: %w", rerr)
		}
		var f struct {
			MsgID string `json:"msgid"`
			Text  struct {
				Content string `json:"content"`
			} `json:"text"`
			From struct {
				UserID string `json:"userid"`
			} `json:"from"`
			ChatID string `json:"chatid"`
		}
		if json.Unmarshal(raw, &f) != nil || f.MsgID == "" || f.ChatID == "" {
			continue
		}
		ack, aerr := json.Marshal(map[string]any{"errcode": 0, "errmsg": "ok"})
		if aerr != nil {
			return fmt.Errorf("wecom: ack encode: %w", aerr)
		}
		if err := ws.Write(ctx, websocket.MessageText, ack); err != nil {
			return fmt.Errorf("wecom: ack: %w", err)
		}
		if !w.dedupe.Claim("wecom:"+f.MsgID, w.now()) {
			continue
		}
		in := gateway.Inbound{
			Platform: "wecom", ChatID: f.ChatID, SenderID: f.From.UserID,
			Text: f.Text.Content, UpdateID: f.MsgID, Group: true, Mentioned: true, Payload: string(raw),
		}
		if derr := gwplat.Dispatch(ctx, w.handler, &in); derr != nil {
			return fmt.Errorf("dispatch: %w", derr)
		}
	}
}

func parseMessage(plain []byte) (gateway.Inbound, bool) {
	var p struct {
		FromUserName string `xml:"FromUserName"`
		MsgType      string `xml:"MsgType"`
		Content      string `xml:"Content"`
		MsgID        string `xml:"MsgId"`
	}
	if parseXML(plain, &p) != nil || p.MsgID == "" || p.MsgType != "text" {
		return gateway.Inbound{}, false
	}
	return gateway.Inbound{
		Platform: "wecom", ChatID: p.FromUserName, SenderID: p.FromUserName,
		Text: p.Content, UpdateID: p.MsgID, Mentioned: true, Payload: string(plain),
	}, true
}

// parseXML rejects DTD/entity declarations before decoding.
func parseXML(data []byte, v any) error {
	low := strings.ToLower(string(data))
	for _, bad := range []string{"<!doctype", "<!entity", "system", "public"} {
		if strings.Contains(low, bad) {
			return ErrExternalEntity
		}
	}
	if err := xml.Unmarshal(data, v); err != nil { // #nosec G709 -- DTD/entities rejected above; encoding/xml never expands them.
		return fmt.Errorf("%w: xml", gwplat.ErrBadPayload)
	}
	return nil
}

func signature(token, timestamp, nonce, encrypt string) string {
	parts := []string{token, timestamp, nonce, encrypt}
	slices.Sort(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, ""))) // #nosec G401 -- WeCom protocol signature.
	return hex.EncodeToString(sum[:])
}

func decrypt(encodingKey, encrypted string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(encodingKey + "=")
	data, derr := base64.StdEncoding.DecodeString(encrypted)
	if err != nil || derr != nil || len(key) != 32 || len(data) < 32 || len(data)%aes.BlockSize != 0 {
		return nil, gwplat.ErrBadSignature
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("wecom: aes: %w", gwplat.ErrBadSignature)
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, key[:aes.BlockSize]).CryptBlocks(out, data)
	pad := int(out[len(out)-1])
	if pad == 0 || pad > aes.BlockSize || subtle.ConstantTimeCompare(out[len(out)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) != 1 {
		return nil, gwplat.ErrBadSignature
	}
	out = out[:len(out)-pad]
	if len(out) < 20 {
		return nil, gwplat.ErrBadSignature
	}
	n := int(binary.BigEndian.Uint32(out[16:20]))
	if n < 0 || 20+n > len(out) {
		return nil, gwplat.ErrBadSignature
	}
	return out[20 : 20+n], nil
}

func closeDialBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	if err := resp.Body.Close(); err != nil {
		return
	}
}

func closeWS(ws *websocket.Conn) {
	if err := ws.CloseNow(); err != nil {
		return
	}
}
