// Package line is the LINE Messaging API adapter of F7 PLAT-010.
package line

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

// ErrConfig rejects invalid activation data.
var ErrConfig = errors.New("line: invalid config")

// Config is explicit activation; the zero value is disabled.
type Config struct {
	Enabled    bool
	API        string
	HTTPClient *http.Client
	Now        func() time.Time
}

// Adapter is one configured LINE channel.
type Adapter struct {
	desc    *gwplat.Descriptor
	out     *gwplat.Outbox
	handler gwplat.Handler
	secret  string
	token   string
	dedupe  *gwplat.Dedupe
	now     func() time.Time
	http    gwplat.HTTP
	api     string
}

// New validates explicit config and secrets; it opens no connection.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource,
	out *gwplat.Outbox, h gwplat.Handler,
) (*Adapter, error) {
	if desc == nil || desc.Name != "line" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("line: activation: %w", err)
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("line: secret: %w", err)
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
		api = "https://api.line.me"
	}
	return &Adapter{
		desc: desc, out: out, handler: h,
		secret: values["line_channel_secret"], token: values["line_access_token"],
		dedupe: gwplat.NewDedupe(4096, 30*time.Minute), now: now,
		http: gwplat.HTTP{Client: client}, api: strings.TrimRight(api, "/"),
	}, nil
}

// Send pushes one rendered reply; a delayed turn never burns a reply token.
func (a *Adapter) Send(ctx context.Context, chat, markdown string) error {
	err := a.out.Reply(ctx, gwplat.Reply{
		Chat: chat, Markdown: markdown,
		Send: func(ctx context.Context, part string) error {
			body := map[string]any{"to": chat, "messages": []map[string]string{{"type": "text", "text": part}}}
			_, err := a.http.DoJSON(ctx, a.api+"/v2/bot/message/push", body,
				map[string]string{"Authorization": "Bearer " + a.token})
			if err != nil {
				return fmt.Errorf("line: %w", err)
			}
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("line: outbox: %w", err)
	}
	return nil
}

// Handler verifies X-Line-Signature before parsing events.
func (a *Adapter) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := a.http.ReadBody(w, r)
		if err != nil {
			return
		}
		if verr := gwplat.VerifyHMACSHA256(a.secret, body, r.Header.Get("X-Line-Signature")); verr != nil {
			gwplat.Deny(w, http.StatusUnauthorized)
			return
		}
		events, err := parse(body)
		if err != nil {
			gwplat.Deny(w, http.StatusBadRequest)
			return
		}
		for i := range events {
			ev := &events[i]
			if !a.dedupe.Claim("line-reply:"+ev.replyToken, a.now()) {
				continue
			}
			if !a.dedupe.Claim("line:"+ev.inbound.UpdateID, a.now()) {
				continue
			}
			if derr := gwplat.Dispatch(r.Context(), a.handler, &ev.inbound); derr != nil {
				gwplat.Deny(w, http.StatusInternalServerError)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
}

type lineEvent struct {
	replyToken string
	inbound    gateway.Inbound
}

func parse(body []byte) ([]lineEvent, error) {
	var p struct {
		Events []struct {
			Type       string                                   `json:"type"`
			ReplyToken string                                   `json:"replyToken"`
			Source     struct{ UserID, GroupID, RoomID string } `json:"source"`
			Message    struct{ ID, Type, Text string }          `json:"message"`
		} `json:"events"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("%w: line json", gwplat.ErrBadPayload)
	}
	out := make([]lineEvent, 0, len(p.Events))
	for i := range p.Events {
		ev := &p.Events[i]
		if ev.Type != "message" || ev.Message.Type != "text" || ev.Message.ID == "" {
			continue
		}
		chat := ev.Source.UserID
		group := false
		if ev.Source.GroupID != "" {
			chat, group = ev.Source.GroupID, true
		} else if ev.Source.RoomID != "" {
			chat, group = ev.Source.RoomID, true
		}
		if chat == "" || ev.ReplyToken == "" {
			continue
		}
		out = append(out, lineEvent{ev.ReplyToken, gateway.Inbound{
			Platform: "line", ChatID: chat,
			SenderID: ev.Source.UserID, Text: ev.Message.Text, UpdateID: ev.Message.ID, Group: group,
			Mentioned: !group || strings.Contains(ev.Message.Text, "@"), Payload: string(body),
		}})
	}
	return out, nil
}
