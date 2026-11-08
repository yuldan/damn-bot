// Package mattermost is the Mattermost REST v4 + WebSocket adapter of F7 PLAT-012.
package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

// ErrConfig rejects invalid activation data.
var ErrConfig = errors.New("mattermost: invalid config")

// Config is explicit activation; the zero value is disabled.
type Config struct {
	Enabled    bool
	BaseURL    string
	WSURL      string
	HTTPClient *http.Client
	Now        func() time.Time
	Backoff    gwplat.Backoff
}

// Adapter is one Mattermost server token.
type Adapter struct {
	desc    *gwplat.Descriptor
	out     *gwplat.Outbox
	handler gwplat.Handler
	token   string
	api     string
	wsURL   string
	dedupe  *gwplat.Dedupe
	now     func() time.Time
	http    gwplat.HTTP
	backoff gwplat.Backoff
}

// New validates explicit config and secrets; it opens no connection.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource,
	out *gwplat.Outbox, h gwplat.Handler,
) (*Adapter, error) {
	if desc == nil || desc.Name != "mattermost" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("mattermost: activation: %w", err)
	}
	if err := validateEndpoint(cfg.BaseURL); err != nil {
		return nil, err
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("mattermost: secret: %w", err)
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
		desc: desc, out: out, handler: h, token: values["mattermost_token"],
		api: strings.TrimRight(cfg.BaseURL, "/"), wsURL: cfg.WSURL,
		dedupe: gwplat.NewDedupe(4096, 30*time.Minute), now: now,
		http: gwplat.HTTP{Client: client}, backoff: cfg.Backoff,
	}, nil
}

func validateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("%w: url", ErrConfig)
	}
	if u.Scheme != "https" && (u.Scheme != "http" || u.Hostname() != "localhost" && !strings.HasPrefix(u.Hostname(), "127.")) {
		return fmt.Errorf("%w: https or loopback required", ErrConfig)
	}
	return nil
}

// Send posts one rendered reply.
func (m *Adapter) Send(ctx context.Context, chat, markdown string) error {
	err := m.out.Reply(ctx, gwplat.Reply{
		Chat: chat, Markdown: markdown,
		Send: func(ctx context.Context, part string) error {
			body := map[string]string{"channel_id": chat, "message": part}
			_, err := m.http.DoJSON(ctx, m.api+"/api/v4/posts", body,
				map[string]string{"Authorization": "Bearer " + m.token})
			if err != nil {
				return fmt.Errorf("mattermost: %w", err)
			}
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("mattermost: outbox: %w", err)
	}
	return nil
}

// Run keeps the websocket alive with capped backoff.
func (m *Adapter) Run(ctx context.Context) error {
	for attempt := 1; ; attempt++ {
		err := m.session(ctx)
		if ctx.Err() != nil {
			if err != nil {
				return fmt.Errorf("mattermost: %w", err)
			}
			return nil
		}
		if waitErr := m.backoff.Wait(ctx, attempt); waitErr != nil {
			return fmt.Errorf("mattermost: reconnect: %w", waitErr)
		}
	}
}

func (m *Adapter) session(ctx context.Context) error {
	rawURL := m.wsURL
	if rawURL == "" {
		u, err := url.Parse(m.api)
		if err != nil {
			return fmt.Errorf("%w: url", ErrConfig)
		}
		u.Scheme = strings.Replace(u.Scheme, "http", "ws", 1)
		u.Path += "/api/v4/websocket"
		rawURL = u.String()
	}
	ws, resp, derr := websocket.Dial(ctx, rawURL, nil)
	closeDialBody(resp)
	if derr != nil {
		return fmt.Errorf("mattermost: ws: %w", derr)
	}
	defer closeWS(ws)
	auth, merr := json.Marshal(map[string]any{
		"seq": 1, "action": "authentication_challenge",
		"data": map[string]string{"token": m.token},
	})
	if merr != nil {
		return fmt.Errorf("mattermost: auth encode: %w", merr)
	}
	if err := ws.Write(ctx, websocket.MessageText, auth); err != nil {
		return fmt.Errorf("mattermost: auth: %w", err)
	}
	for {
		_, raw, rerr := ws.Read(ctx)
		if rerr != nil {
			return fmt.Errorf("mattermost: read: %w", rerr)
		}
		var ev struct {
			Event string `json:"event"`
			Data  struct {
				Post string `json:"post"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &ev) != nil || ev.Event != "posted" {
			continue
		}
		var post struct {
			ID        string `json:"id"`
			Message   string `json:"message"`
			ChannelID string `json:"channel_id"`
			UserID    string `json:"user_id"`
		}
		if json.Unmarshal([]byte(ev.Data.Post), &post) != nil || post.ID == "" || post.ChannelID == "" {
			continue
		}
		if !m.dedupe.Claim("mattermost:"+post.ID, m.now()) {
			continue
		}
		in := gateway.Inbound{
			Platform: "mattermost", ChatID: post.ChannelID, SenderID: post.UserID,
			Text: post.Message, UpdateID: post.ID, Mentioned: true, Payload: ev.Data.Post,
		}
		if derr := gwplat.Dispatch(ctx, m.handler, &in); derr != nil {
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
