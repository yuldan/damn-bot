// Package googlechat is the Google Chat callback adapter of F7 PLAT-008.
package googlechat

import (
	"cmp"
	"context"
	"crypto/rsa"
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
var ErrConfig = errors.New("googlechat: invalid config")

// ErrNoToken reports a missing OAuth token source.
var ErrNoToken = errors.New("googlechat: no token source")

// Config is explicit activation; the zero value is disabled.
type Config struct {
	Enabled     bool
	API         string
	HTTPClient  *http.Client
	Now         func() time.Time
	JWTKeys     map[string]*rsa.PublicKey
	JWTIssuer   string
	JWTAudience string
	Token       func(context.Context) (string, error)
}

// Adapter is one Google Chat app.
type Adapter struct {
	desc    *gwplat.Descriptor
	out     *gwplat.Outbox
	handler gwplat.Handler
	keys    map[string]*rsa.PublicKey
	issuer  string
	aud     string
	token   func(context.Context) (string, error)
	api     string
	dedupe  *gwplat.Dedupe
	now     func() time.Time
	http    gwplat.HTTP
}

// New validates explicit config and secrets; it opens no connection.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource,
	out *gwplat.Outbox, h gwplat.Handler,
) (*Adapter, error) {
	if desc == nil || desc.Name != "googlechat" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("googlechat: activation: %w", err)
	}
	if _, err := gwplat.ResolveSecrets(ctx, desc, secrets); err != nil {
		return nil, fmt.Errorf("googlechat: secret: %w", err)
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
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
		desc: desc, out: out, handler: h, keys: cfg.JWTKeys,
		issuer: cmp.Or(cfg.JWTIssuer, "chat@system.gserviceaccount.com"), aud: cfg.JWTAudience,
		token: cfg.Token, api: cmp.Or(cfg.API, "https://chat.googleapis.com"),
		dedupe: gwplat.NewDedupe(4096, 30*time.Minute), now: now,
		http: gwplat.HTTP{Client: client},
	}, nil
}

// Send posts one rendered reply to a space.
func (a *Adapter) Send(ctx context.Context, chat, markdown string) error {
	if a.token == nil {
		return ErrNoToken
	}
	tok, err := a.token(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNoToken, err)
	}
	err = a.out.Reply(ctx, gwplat.Reply{
		Chat: chat, Markdown: markdown,
		Send: func(ctx context.Context, part string) error {
			_, derr := a.http.DoJSON(ctx, a.api+"/v1/"+chat+"/messages", map[string]string{"text": part},
				map[string]string{"Authorization": "Bearer " + tok})
			if derr != nil {
				return fmt.Errorf("googlechat: %w", derr)
			}
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("googlechat: outbox: %w", err)
	}
	return nil
}

// Handler verifies the Google JWT before dispatch.
func (a *Adapter) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := a.http.ReadBody(w, r)
		if err != nil {
			return
		}
		if _, verr := gwplat.VerifyJWT(gwplat.Bearer(r), a.keys, a.issuer, a.aud, a.now()); verr != nil {
			gwplat.Deny(w, http.StatusUnauthorized)
			return
		}
		in, ok := parse(body)
		if !ok {
			gwplat.Deny(w, http.StatusBadRequest)
			return
		}
		if !a.dedupe.Claim("googlechat:"+in.UpdateID, a.now()) {
			w.WriteHeader(http.StatusOK)
			return
		}
		if derr := gwplat.Dispatch(r.Context(), a.handler, &in); derr != nil {
			gwplat.Deny(w, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func parse(body []byte) (gateway.Inbound, bool) {
	var p struct {
		Type    string `json:"type"`
		Message struct {
			Name   string `json:"name"`
			Text   string `json:"text"`
			Sender struct {
				Name string `json:"name"`
			} `json:"sender"`
		} `json:"message"`
		Space struct{ Name, Type string } `json:"space"`
		User  struct {
			Name string `json:"name"`
		} `json:"user"`
	}
	if json.Unmarshal(body, &p) != nil || p.Type != "MESSAGE" || p.Message.Name == "" || p.Space.Name == "" {
		return gateway.Inbound{}, false
	}
	sender := cmp.Or(p.Message.Sender.Name, p.User.Name)
	group := p.Space.Type == "ROOM"
	return gateway.Inbound{
		Platform: "googlechat", ChatID: p.Space.Name, SenderID: sender,
		Text: p.Message.Text, UpdateID: p.Message.Name, Group: group,
		Mentioned: !group || strings.Contains(p.Message.Text, "@"), Payload: string(body),
	}, true
}
