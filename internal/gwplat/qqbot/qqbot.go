// Package qqbot is the QQ Bot API v2 adapter of F7 PLAT-015.
package qqbot

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

// ErrConfig rejects invalid activation data.
var ErrConfig = errors.New("qqbot: invalid config")

// Config is explicit activation; the zero value is disabled.
type Config struct {
	Enabled    bool
	API        string
	TokenAPI   string
	HTTPClient *http.Client
	Now        func() time.Time
	Backoff    gwplat.Backoff
}

// Adapter is one QQ application.
type Adapter struct {
	desc     *gwplat.Descriptor
	out      *gwplat.Outbox
	handler  gwplat.Handler
	appID    string
	secret   string
	api      string
	tokenAPI string
	dedupe   *gwplat.Dedupe
	now      func() time.Time
	http     gwplat.HTTP
	backoff  gwplat.Backoff

	mu      sync.Mutex
	token   string
	expires time.Time
}

// New validates explicit config and secrets; it opens no connection.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource,
	out *gwplat.Outbox, h gwplat.Handler,
) (*Adapter, error) {
	if desc == nil || desc.Name != "qqbot" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("qqbot: activation: %w", err)
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("qqbot: secret: %w", err)
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
		desc: desc, out: out, handler: h, appID: values["qqbot_app_id"],
		secret: values["qqbot_client_secret"], api: cmp.Or(cfg.API, "https://api.sgroup.qq.com"),
		tokenAPI: cmp.Or(cfg.TokenAPI, "https://bots.qq.com"),
		dedupe:   gwplat.NewDedupe(4096, 30*time.Minute), now: now,
		http: gwplat.HTTP{Client: client}, backoff: cfg.Backoff,
	}, nil
}

func (a *Adapter) accessToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && a.now().Before(a.expires.Add(-time.Minute)) {
		return a.token, nil
	}
	data, err := a.http.DoJSON(ctx, a.tokenAPI+"/app/getAppAccessToken",
		map[string]string{"appId": a.appID, "clientSecret": a.secret}, nil)
	if err != nil {
		return "", fmt.Errorf("qqbot: %w", err)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(data, &out) != nil || out.AccessToken == "" {
		return "", fmt.Errorf("%w: qqbot token", gwplat.ErrBadPayload)
	}
	a.token, a.expires = out.AccessToken, a.now().Add(time.Hour)
	return a.token, nil
}

// Send posts one rendered reply to the channel.
func (a *Adapter) Send(ctx context.Context, chat, markdown string) error {
	err := a.out.Reply(ctx, gwplat.Reply{
		Chat: chat, Markdown: markdown,
		Send: func(ctx context.Context, part string) error {
			tok, err := a.accessToken(ctx)
			if err != nil {
				return fmt.Errorf("qqbot: %w", err)
			}
			body := map[string]any{"content": part, "msg_type": 0}
			_, err = a.http.DoJSON(ctx, a.api+"/v2/channels/"+url.PathEscape(chat)+"/messages", body,
				map[string]string{"Authorization": "QQBot " + tok})
			if err != nil {
				return fmt.Errorf("qqbot: %w", err)
			}
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("qqbot: outbox: %w", err)
	}
	return nil
}

// Run keeps the gateway websocket alive with capped backoff.
func (a *Adapter) Run(ctx context.Context) error {
	for attempt := 1; ; attempt++ {
		err := a.session(ctx)
		if ctx.Err() != nil {
			return err
		}
		if waitErr := a.backoff.Wait(ctx, attempt); waitErr != nil {
			return fmt.Errorf("qqbot: reconnect: %w", waitErr)
		}
		_ = err
	}
}

func (a *Adapter) session(ctx context.Context) error {
	tok, err := a.accessToken(ctx)
	if err != nil {
		return fmt.Errorf("qqbot: %w", err)
	}
	data, err := a.http.Do(ctx, http.MethodGet, a.api+"/gateway", nil,
		map[string]string{"Authorization": "QQBot " + tok})
	if err != nil {
		return fmt.Errorf("qqbot: %w", err)
	}
	var out struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(data, &out) != nil || out.URL == "" {
		return fmt.Errorf("%w: qqbot gateway", gwplat.ErrBadPayload)
	}
	ws, resp, derr := websocket.Dial(ctx, out.URL, nil)
	closeDialBody(resp)
	if derr != nil {
		return fmt.Errorf("qqbot: ws: %w", derr)
	}
	defer closeWS(ws)
	identify, ierr := json.Marshal(map[string]any{"op": 2, "d": map[string]any{"token": "QQBot " + tok, "intents": 1 << 25}})
	if ierr != nil {
		return fmt.Errorf("qqbot: identify encode: %w", ierr)
	}
	if err := ws.Write(ctx, websocket.MessageText, identify); err != nil {
		return fmt.Errorf("qqbot: identify: %w", err)
	}
	for {
		_, raw, rerr := ws.Read(ctx)
		if rerr != nil {
			return fmt.Errorf("qqbot: read: %w", rerr)
		}
		var frame struct {
			Op int    `json:"op"`
			T  string `json:"t"`
			ID string `json:"id"`
			D  struct {
				ID        string `json:"id"`
				Content   string `json:"content"`
				ChannelID string `json:"channel_id"`
				Author    struct {
					ID string `json:"id"`
				} `json:"author"`
			} `json:"d"`
		}
		if json.Unmarshal(raw, &frame) != nil || frame.Op != 0 || frame.T != "AT_MESSAGE_CREATE" {
			continue
		}
		id := cmp.Or(frame.ID, frame.D.ID)
		if !a.dedupe.Claim("qqbot:"+id, a.now()) {
			continue
		}
		in := gateway.Inbound{
			Platform: "qqbot", ChatID: frame.D.ChannelID, SenderID: frame.D.Author.ID,
			Text: frame.D.Content, UpdateID: id, Group: true, Mentioned: true, Payload: string(raw),
		}
		if derr := gwplat.Dispatch(ctx, a.handler, &in); derr != nil {
			return fmt.Errorf("dispatch: %w", derr)
		}
	}
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

var _ = strings.TrimSpace
