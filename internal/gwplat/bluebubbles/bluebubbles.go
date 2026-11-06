// Package bluebubbles is the BlueBubbles (iMessage server) adapter of F7
// PLAT-002: REST send plus an authenticated new-message webhook.
package bluebubbles

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

// ErrConfig rejects invalid activation data.
var ErrConfig = errors.New("bluebubbles: invalid config")

// Config is explicit activation; the zero value is disabled.
type Config struct {
	Enabled    bool
	BaseURL    string
	HTTPClient *http.Client
	Now        func() time.Time
}

// Adapter is one configured BlueBubbles server.
type Adapter struct {
	cfg      Config
	desc     *gwplat.Descriptor
	out      *gwplat.Outbox
	handler  gwplat.Handler
	password string
	dedupe   *gwplat.Dedupe
	now      func() time.Time
	http     gwplat.HTTP
	api      string
}

// New validates explicit config and secrets; it opens no connection.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource,
	out *gwplat.Outbox, h gwplat.Handler,
) (*Adapter, error) {
	if desc == nil || desc.Name != "bluebubbles" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("bluebubbles: activation: %w", err)
	}
	api := strings.TrimRight(cfg.BaseURL, "/")
	if err := validateEndpoint(api); err != nil {
		return nil, err
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("bluebubbles: secret: %w", err)
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
		cfg: *cfg, desc: desc, out: out, handler: h,
		password: values["bluebubbles_password"], dedupe: gwplat.NewDedupe(4096, 30*time.Minute),
		now: now, http: gwplat.HTTP{Client: client}, api: api,
	}, nil
}

func validateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: url", ErrConfig)
	}
	if u.Scheme == "https" {
		return nil
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if u.Scheme == "http" && (host == "localhost" || ip != nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("%w: https or loopback required", ErrConfig)
}

// Send posts one rendered text through the BlueBubbles REST API.
func (a *Adapter) Send(ctx context.Context, chat, markdown string) error {
	err := a.out.Reply(ctx, gwplat.Reply{
		Chat: chat, Markdown: markdown,
		Send: func(ctx context.Context, part string) error {
			q := "?password=" + url.QueryEscape(a.password)
			body := map[string]string{"chatGuid": chat, "tempGuid": fmt.Sprintf("damnbot-%d", a.now().UnixNano()), "message": part}
			_, err := a.http.DoJSON(ctx, a.api+"/api/v1/message/text"+q, body, nil)
			return fmt.Errorf("bluebubbles: send: %w", err)
		},
	})
	if err != nil {
		return fmt.Errorf("bluebubbles: outbox: %w", err)
	}
	return nil
}

// Handler receives the BlueBubbles new-message webhook.
func (a *Adapter) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := a.http.ReadBody(w, r)
		if err != nil {
			return
		}
		got := cmp.Or(r.URL.Query().Get("password"), r.Header.Get("X-BlueBubbles-Password"))
		if !gwplat.ConstantTimeEqual(got, a.password) {
			gwplat.Deny(w, http.StatusUnauthorized)
			return
		}
		in, ok := parse(body)
		if !ok {
			gwplat.Deny(w, http.StatusBadRequest)
			return
		}
		if !a.dedupe.Claim("bluebubbles:"+in.UpdateID, a.now()) {
			w.WriteHeader(http.StatusOK)
			return
		}
		if err := gwplat.Dispatch(r.Context(), a.handler, &in); err != nil {
			gwplat.Deny(w, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func parse(body []byte) (gateway.Inbound, bool) {
	var p struct {
		Data struct {
			GUID     string `json:"guid"`
			Text     string `json:"text"`
			IsFromMe bool   `json:"isFromMe"`
			Handle   struct {
				Address string `json:"address"`
			} `json:"handle"`
			Chats []struct {
				GUID string `json:"guid"`
			} `json:"chats"`
			IsGroup bool `json:"isGroup"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &p) != nil {
		return gateway.Inbound{}, false
	}
	var chat string
	if len(p.Data.Chats) > 0 {
		chat = p.Data.Chats[0].GUID
	}
	if p.Data.GUID == "" || chat == "" || p.Data.Handle.Address == "" || p.Data.IsFromMe {
		return gateway.Inbound{}, false
	}
	return gateway.Inbound{
		Platform: "bluebubbles", ChatID: chat, SenderID: p.Data.Handle.Address,
		Text: p.Data.Text, UpdateID: p.Data.GUID, Payload: string(body),
		Group: p.Data.IsGroup || strings.Contains(chat, ";+;"), Mentioned: true,
	}, true
}
