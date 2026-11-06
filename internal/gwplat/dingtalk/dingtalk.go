// Package dingtalk is the DingTalk Stream Mode adapter of F7 PLAT-004.
package dingtalk

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

// ErrConfig rejects invalid activation data.
var ErrConfig = errors.New("dingtalk: invalid config")

// Config is explicit activation; the zero value is disabled.
type Config struct {
	Enabled    bool
	API        string
	HTTPClient *http.Client
	Now        func() time.Time
	Backoff    gwplat.Backoff
}

// Adapter is one DingTalk application.
type Adapter struct {
	desc    *gwplat.Descriptor
	out     *gwplat.Outbox
	handler gwplat.Handler
	client  string
	secret  string
	api     string
	dedupe  *gwplat.Dedupe
	now     func() time.Time
	http    gwplat.HTTP
	backoff gwplat.Backoff

	mu      sync.Mutex
	token   string
	expires time.Time
}

// New validates explicit config and secrets; it opens no connection.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource,
	out *gwplat.Outbox, h gwplat.Handler,
) (*Adapter, error) {
	if desc == nil || desc.Name != "dingtalk" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("dingtalk: activation: %w", err)
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("dingtalk: secret: %w", err)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	client := cfg.HTTPClient
	if client == nil {
		client = netx.LoopbackClient()
	}
	return &Adapter{
		desc: desc, out: out, handler: h, client: values["dingtalk_client_id"],
		secret: values["dingtalk_client_secret"], api: cmp.Or(cfg.API, "https://api.dingtalk.com"),
		dedupe: gwplat.NewDedupe(4096, 30*time.Minute), now: now,
		http: gwplat.HTTP{Client: client}, backoff: cfg.Backoff,
	}, nil
}

func (a *Adapter) accessToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && a.now().Before(a.expires.Add(-time.Minute)) {
		return a.token, nil
	}
	data, err := a.http.DoJSON(ctx, a.api+"/v1.0/oauth2/accessToken",
		map[string]string{"appKey": a.client, "appSecret": a.secret}, nil)
	if err != nil {
		return "", fmt.Errorf("dingtalk: token request: %w", err)
	}
	var out struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int64  `json:"expireIn"`
	}
	if json.Unmarshal(data, &out) != nil || out.AccessToken == "" {
		return "", fmt.Errorf("%w: dingtalk token", gwplat.ErrBadPayload)
	}
	a.token, a.expires = out.AccessToken, a.now().Add(time.Duration(out.ExpireIn)*time.Second)
	return a.token, nil
}

// Send posts one rendered reply.
func (a *Adapter) Send(ctx context.Context, chat, markdown string) error {
	err := a.out.Reply(ctx, gwplat.Reply{
		Chat: chat, Markdown: markdown,
		Send: func(ctx context.Context, part string) error {
			tok, err := a.accessToken(ctx)
			if err != nil {
				return fmt.Errorf("dingtalk: %w", err)
			}
			param, merr := json.Marshal(map[string]string{"content": part})
			if merr != nil {
				return fmt.Errorf("dingtalk: encode: %w", merr)
			}
			body := map[string]any{
				"robotCode": a.client, "userIds": []string{chat},
				"msgKey": "sampleText", "msgParam": string(param),
			}
			_, err = a.http.DoJSON(ctx, a.api+"/v1.0/robot/oToMessages/batchSend", body,
				map[string]string{"x-acs-dingtalk-access-token": tok})
			return fmt.Errorf("dingtalk: send: %w", err)
		},
	})
	if err != nil {
		return fmt.Errorf("dingtalk: outbox: %w", err)
	}
	return nil
}

// Run keeps the Stream Mode websocket alive with capped backoff.
func (a *Adapter) Run(ctx context.Context) error {
	for attempt := 1; ; attempt++ {
		err := a.session(ctx)
		if ctx.Err() != nil {
			if err != nil {
				return fmt.Errorf("dingtalk: %w", err)
			}
			return nil
		}
		if waitErr := a.backoff.Wait(ctx, attempt); waitErr != nil {
			return fmt.Errorf("dingtalk: reconnect: %w", waitErr)
		}
	}
}

func (a *Adapter) session(ctx context.Context) error {
	tok, err := a.accessToken(ctx)
	if err != nil {
		return fmt.Errorf("dingtalk: %w", err)
	}
	data, err := a.http.DoJSON(ctx, a.api+"/v1.0/gateway/connections/open",
		map[string]string{"clientId": a.client, "clientSecret": a.secret},
		map[string]string{"x-acs-dingtalk-access-token": tok})
	if err != nil {
		return fmt.Errorf("dingtalk: %w", err)
	}
	var conn struct{ Endpoint, Ticket string }
	if uerr := json.Unmarshal(data, &conn); uerr != nil || conn.Endpoint == "" {
		return fmt.Errorf("%w: dingtalk gateway", gwplat.ErrBadPayload)
	}
	ws, resp, derr := websocket.Dial(ctx, conn.Endpoint+"?ticket="+conn.Ticket, nil)
	closeDialBody(resp)
	if derr != nil {
		return fmt.Errorf("dingtalk: ws: %w", derr)
	}
	defer closeWS(ws)
	return a.serve(ctx, ws)
}

func parse(data json.RawMessage) (gateway.Inbound, bool) {
	var p struct {
		MsgID          string `json:"msgId"`
		ConversationID string `json:"conversationId"`
		SenderID       string `json:"senderId"`
		ConvType       string `json:"conversationType"`
		Text           struct {
			Content string `json:"content"`
		} `json:"text"`
	}
	if json.Unmarshal(data, &p) != nil || p.MsgID == "" || p.ConversationID == "" {
		return gateway.Inbound{}, false
	}
	group := p.ConvType != "" && p.ConvType != "1"
	return gateway.Inbound{
		Platform: "dingtalk", ChatID: p.ConversationID, SenderID: p.SenderID,
		Text: p.Text.Content, UpdateID: p.MsgID, Group: group,
		Mentioned: !group || strings.Contains(p.Text.Content, "@"), Payload: string(data),
	}, true
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

func (a *Adapter) serve(ctx context.Context, ws *websocket.Conn) error {
	for {
		_, raw, rerr := ws.Read(ctx)
		if rerr != nil {
			return fmt.Errorf("dingtalk: read: %w", rerr)
		}
		var frame struct {
			Headers struct {
				MessageID string `json:"messageId"`
			} `json:"headers"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(raw, &frame) != nil || frame.Headers.MessageID == "" {
			continue
		}
		ack, aerr := json.Marshal(map[string]any{"code": 200, "headers": map[string]string{
			"messageId": frame.Headers.MessageID,
		}, "message": "OK"})
		if aerr != nil {
			return fmt.Errorf("dingtalk: ack encode: %w", aerr)
		}
		if err := ws.Write(ctx, websocket.MessageText, ack); err != nil {
			return fmt.Errorf("dingtalk: ack: %w", err)
		}
		if in, ok := parse(frame.Data); ok {
			if a.dedupe.Claim("dingtalk:"+in.UpdateID, a.now()) {
				if derr := gwplat.Dispatch(ctx, a.handler, &in); derr != nil {
					return fmt.Errorf("dispatch: %w", derr)
				}
			}
		}
	}
}
