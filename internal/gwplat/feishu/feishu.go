// Package feishu is the Feishu/Lark adapter of F7 PLAT-007.
package feishu

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
var ErrConfig = errors.New("feishu: invalid config")

// Config is explicit activation; the zero value is disabled.
type Config struct {
	Enabled    bool
	API        string
	WSURL      string
	HTTPClient *http.Client
	Now        func() time.Time
	Backoff    gwplat.Backoff
}

// Adapter is one Feishu application.
type Adapter struct {
	desc         *gwplat.Descriptor
	out          *gwplat.Outbox
	handler      gwplat.Handler
	appID        string
	appSecret    string
	verification string
	api          string
	wsURL        string
	dedupe       *gwplat.Dedupe
	now          func() time.Time
	http         gwplat.HTTP
	backoff      gwplat.Backoff

	mu      sync.Mutex
	token   string
	expires time.Time
}

// New validates explicit config and secrets; it opens no connection.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource,
	out *gwplat.Outbox, h gwplat.Handler,
) (*Adapter, error) {
	if desc == nil || desc.Name != "feishu" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("feishu: activation: %w", err)
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("feishu: secret: %w", err)
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
		desc: desc, out: out, handler: h, appID: values["feishu_app_id"],
		appSecret: values["feishu_app_secret"], verification: values["feishu_verification_token"],
		api: cmp.Or(cfg.API, "https://open.feishu.cn"), wsURL: cfg.WSURL,
		dedupe: gwplat.NewDedupe(4096, 30*time.Minute), now: now,
		http: gwplat.HTTP{Client: client}, backoff: cfg.Backoff,
	}, nil
}

func (a *Adapter) tenantToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && a.now().Before(a.expires.Add(-time.Minute)) {
		return a.token, nil
	}
	data, err := a.http.DoJSON(ctx, a.api+"/open-apis/auth/v3/tenant_access_token/internal",
		map[string]string{"app_id": a.appID, "app_secret": a.appSecret}, nil)
	if err != nil {
		return "", fmt.Errorf("feishu: %w", err)
	}
	var out struct {
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int64  `json:"expire"`
	}
	if json.Unmarshal(data, &out) != nil || out.TenantAccessToken == "" {
		return "", fmt.Errorf("%w: feishu token", gwplat.ErrBadPayload)
	}
	a.token, a.expires = out.TenantAccessToken, a.now().Add(time.Duration(out.Expire)*time.Second)
	return a.token, nil
}

// Send posts one rendered reply.
func (a *Adapter) Send(ctx context.Context, chat, markdown string) error {
	err := a.out.Reply(ctx, gwplat.Reply{
		Chat: chat, Markdown: markdown,
		Send: func(ctx context.Context, part string) error {
			tok, err := a.tenantToken(ctx)
			if err != nil {
				return fmt.Errorf("feishu: %w", err)
			}
			content, cerr := json.Marshal(map[string]string{"text": part})
			if cerr != nil {
				return fmt.Errorf("feishu: encode: %w", cerr)
			}
			body := map[string]string{"receive_id": chat, "msg_type": "text", "content": string(content)}
			_, err = a.http.DoJSON(ctx, a.api+"/open-apis/im/v1/messages?receive_id_type=chat_id", body,
				map[string]string{"Authorization": "Bearer " + tok})
			if err != nil {
				return fmt.Errorf("feishu: %w", err)
			}
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("feishu: outbox: %w", err)
	}
	return nil
}

// Handler is the webhook fallback; it verifies the event token first.
func (a *Adapter) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := a.http.ReadBody(w, r)
		if err != nil {
			return
		}
		var p struct {
			Challenge string `json:"challenge"`
			Header    struct {
				EventID string `json:"event_id"`
				Token   string `json:"token"`
			} `json:"header"`
			Event json.RawMessage `json:"event"`
		}
		if json.Unmarshal(body, &p) != nil {
			gwplat.Deny(w, http.StatusBadRequest)
			return
		}
		if !gwplat.ConstantTimeEqual(p.Header.Token, a.verification) {
			gwplat.Deny(w, http.StatusUnauthorized)
			return
		}
		if p.Challenge != "" {
			if _, werr := fmt.Fprintf(w, `{"challenge":%q}`, p.Challenge); werr != nil {
				return
			}
			return
		}
		if in, ok := parse(p.Header.EventID, p.Event); ok && a.dedupe.Claim("feishu:"+in.UpdateID, a.now()) {
			if derr := gwplat.Dispatch(r.Context(), a.handler, &in); derr != nil {
				gwplat.Deny(w, http.StatusInternalServerError)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
}

// Run is the long-connection fallback with capped backoff.
func (a *Adapter) Run(ctx context.Context) error {
	if a.wsURL == "" {
		return fmt.Errorf("%w: ws url", ErrConfig)
	}
	for attempt := 1; ; attempt++ {
		err := a.session(ctx)
		if ctx.Err() != nil {
			if err != nil {
				return fmt.Errorf("feishu: %w", err)
			}
			return nil
		}
		if waitErr := a.backoff.Wait(ctx, attempt); waitErr != nil {
			return fmt.Errorf("feishu: reconnect: %w", waitErr)
		}
	}
}

func (a *Adapter) session(ctx context.Context) error {
	ws, resp, derr := websocket.Dial(ctx, a.wsURL, nil)
	closeDialBody(resp)
	if derr != nil {
		return fmt.Errorf("feishu: ws: %w", derr)
	}
	defer closeWS(ws)
	for {
		_, raw, rerr := ws.Read(ctx)
		if rerr != nil {
			return fmt.Errorf("feishu: read: %w", rerr)
		}
		var env struct {
			Header struct {
				EventID string `json:"event_id"`
			} `json:"header"`
			Event json.RawMessage `json:"event"`
		}
		if json.Unmarshal(raw, &env) != nil {
			continue
		}
		if err := ws.Write(ctx, websocket.MessageText, []byte(`{"code":0}`)); err != nil {
			return fmt.Errorf("feishu: ack: %w", err)
		}
		if in, ok := parse(env.Header.EventID, env.Event); ok && a.dedupe.Claim("feishu:"+in.UpdateID, a.now()) {
			if derr := gwplat.Dispatch(ctx, a.handler, &in); derr != nil {
				return fmt.Errorf("dispatch: %w", derr)
			}
		}
	}
}

func parse(eventID string, raw json.RawMessage) (gateway.Inbound, bool) {
	var ev struct {
		Message struct {
			MessageID string `json:"message_id"`
			ChatID    string `json:"chat_id"`
			ChatType  string `json:"chat_type"`
			Content   string `json:"content"`
		} `json:"message"`
		Sender struct {
			SenderID struct {
				OpenID string `json:"open_id"`
			} `json:"sender_id"`
		} `json:"sender"`
	}
	if json.Unmarshal(raw, &ev) != nil || eventID == "" || ev.Message.ChatID == "" {
		return gateway.Inbound{}, false
	}
	var text struct {
		Text string `json:"text"`
	}
	if uerr := json.Unmarshal([]byte(ev.Message.Content), &text); uerr != nil {
		return gateway.Inbound{}, false
	}
	group := ev.Message.ChatType != "p2p"
	return gateway.Inbound{
		Platform: "feishu", ChatID: ev.Message.ChatID, SenderID: ev.Sender.SenderID.OpenID,
		Text: text.Text, UpdateID: eventID, Group: group,
		Mentioned: !group || strings.Contains(text.Text, "@"), Payload: string(raw),
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
