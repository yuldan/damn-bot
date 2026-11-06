// Package slack is the Slack Socket Mode adapter of F7 PLAT-019.
package slack

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

// ErrConfig rejects invalid activation data.
var ErrConfig = errors.New("slack: invalid config")

// Config is explicit activation; the zero value is disabled.
type Config struct {
	Enabled    bool
	API        string
	HTTPClient *http.Client
	Now        func() time.Time
	Backoff    gwplat.Backoff
}

// Adapter is one Slack workspace app.
type Adapter struct {
	desc     *gwplat.Descriptor
	out      *gwplat.Outbox
	handler  gwplat.Handler
	botToken string
	appToken string
	api      string
	dedupe   *gwplat.Dedupe
	now      func() time.Time
	http     gwplat.HTTP
	backoff  gwplat.Backoff
}

// New validates explicit config and secrets; it opens no connection.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource,
	out *gwplat.Outbox, h gwplat.Handler,
) (*Adapter, error) {
	if desc == nil || desc.Name != "slack" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("slack: activation: %w", err)
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("slack: secret: %w", err)
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
		desc: desc, out: out, handler: h, botToken: values["slack_bot_token"],
		appToken: values["slack_app_token"], api: cmp.Or(cfg.API, "https://slack.com/api"),
		dedupe: gwplat.NewDedupe(4096, 30*time.Minute), now: now,
		http: gwplat.HTTP{Client: client}, backoff: cfg.Backoff,
	}, nil
}

// Send posts one rendered mrkdwn reply.
func (s *Adapter) Send(ctx context.Context, chat, markdown string) error {
	err := s.out.Reply(ctx, gwplat.Reply{
		Chat: chat, Markdown: markdown,
		Send: func(ctx context.Context, part string) error {
			body := map[string]string{"channel": chat, "text": part}
			_, err := s.http.DoJSON(ctx, s.api+"/chat.postMessage", body,
				map[string]string{"Authorization": "Bearer " + s.botToken})
			if err != nil {
				return fmt.Errorf("slack: %w", err)
			}
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("slack: outbox: %w", err)
	}
	return nil
}

// Run keeps the Socket Mode connection alive with capped backoff.
func (s *Adapter) Run(ctx context.Context) error {
	for attempt := 1; ; attempt++ {
		err := s.session(ctx)
		if ctx.Err() != nil {
			if err != nil {
				return fmt.Errorf("slack: %w", err)
			}
			return nil
		}
		if waitErr := s.backoff.Wait(ctx, attempt); waitErr != nil {
			return fmt.Errorf("slack: reconnect: %w", waitErr)
		}
	}
}

func (s *Adapter) session(ctx context.Context) error {
	data, err := s.http.DoJSON(ctx, s.api+"/apps.connections.open", map[string]string{},
		map[string]string{"Authorization": "Bearer " + s.appToken})
	if err != nil {
		return fmt.Errorf("slack: %w", err)
	}
	var out struct {
		OK  bool   `json:"ok"`
		URL string `json:"url"`
	}
	if json.Unmarshal(data, &out) != nil || !out.OK || out.URL == "" {
		return fmt.Errorf("%w: connections.open", gwplat.ErrBadPayload)
	}
	ws, resp, derr := websocket.Dial(ctx, out.URL, nil)
	closeDialBody(resp)
	if derr != nil {
		return fmt.Errorf("slack: ws: %w", derr)
	}
	defer closeWS(ws)
	return s.serve(ctx, ws)
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

func (s *Adapter) serve(ctx context.Context, ws *websocket.Conn) error {
	for {
		_, raw, rerr := ws.Read(ctx)
		if rerr != nil {
			return fmt.Errorf("slack: read: %w", rerr)
		}
		var env struct {
			EnvelopeID string `json:"envelope_id"`
			Type       string `json:"type"`
			Payload    struct {
				EventID string `json:"event_id"`
				Event   struct {
					Type    string `json:"type"`
					Subtype string `json:"subtype"`
					Text    string `json:"text"`
					Channel string `json:"channel"`
					User    string `json:"user"`
					TS      string `json:"ts"`
				} `json:"event"`
			} `json:"payload"`
		}
		if json.Unmarshal(raw, &env) != nil || env.EnvelopeID == "" {
			continue
		}
		ack, aerr := json.Marshal(map[string]string{"envelope_id": env.EnvelopeID})
		if aerr != nil {
			return fmt.Errorf("slack: ack encode: %w", aerr)
		}
		if err := ws.Write(ctx, websocket.MessageText, ack); err != nil {
			return fmt.Errorf("slack: ack: %w", err)
		}
		ev := env.Payload.Event
		if env.Type != "events_api" || ev.Type != "message" || ev.Subtype != "" || ev.Channel == "" {
			continue
		}
		id := cmp.Or(env.Payload.EventID, ev.TS)
		if !s.dedupe.Claim("slack:"+id, s.now()) {
			continue
		}
		in := gateway.Inbound{
			Platform: "slack", ChatID: ev.Channel, SenderID: ev.User, Text: ev.Text,
			UpdateID: id, Mentioned: true, Payload: string(raw),
		}
		if derr := gwplat.Dispatch(ctx, s.handler, &in); derr != nil {
			return fmt.Errorf("dispatch: %w", derr)
		}
	}
}
