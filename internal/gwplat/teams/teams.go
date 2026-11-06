// Package teams is the Microsoft Teams Bot Framework adapter of F7 PLAT-021.
package teams

import (
	"cmp"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

// ErrConfig rejects invalid activation data.
var ErrConfig = errors.New("teams: invalid config")

// Config is explicit activation; the zero value is disabled.
type Config struct {
	Enabled    bool
	TokenURL   string
	HTTPClient *http.Client
	Now        func() time.Time
	JWTKeys    map[string]*rsa.PublicKey
	JWTIssuer  string
	Tenants    []string
}

// Adapter is one Bot Framework app.
type Adapter struct {
	desc     *gwplat.Descriptor
	out      *gwplat.Outbox
	handler  gwplat.Handler
	appID    string
	password string
	tokenURL string
	issuer   string
	tenants  []string
	keys     map[string]*rsa.PublicKey
	dedupe   *gwplat.Dedupe
	now      func() time.Time
	http     gwplat.HTTP

	mu      sync.Mutex
	token   string
	expires time.Time
	svc     map[string]string
}

// New validates explicit config and secrets; it opens no connection.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource,
	out *gwplat.Outbox, h gwplat.Handler,
) (*Adapter, error) {
	if desc == nil || desc.Name != "teams" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("teams: activation: %w", err)
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("teams: secret: %w", err)
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
		desc: desc, out: out, handler: h, appID: values["teams_app_id"],
		password: values["teams_app_password"],
		tokenURL: cmp.Or(cfg.TokenURL, "https://login.microsoftonline.com/botframework.com/oauth2/v2.0/token"),
		issuer:   cmp.Or(cfg.JWTIssuer, "https://api.botframework.com"), tenants: cfg.Tenants, keys: cfg.JWTKeys,
		dedupe: gwplat.NewDedupe(4096, 30*time.Minute), now: now,
		http: gwplat.HTTP{Client: client}, svc: map[string]string{},
	}, nil
}

func (t *Adapter) accessToken(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && t.now().Before(t.expires.Add(-time.Minute)) {
		return t.token, nil
	}
	form := url.Values{
		"grant_type": {"client_credentials"}, "client_id": {t.appID},
		"client_secret": {t.password}, "scope": {"https://api.botframework.com/.default"},
	}
	data, err := t.http.Do(ctx, http.MethodPost, t.tokenURL, []byte(form.Encode()),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if err != nil {
		return "", fmt.Errorf("teams: %w", err)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if json.Unmarshal(data, &out) != nil || out.AccessToken == "" {
		return "", fmt.Errorf("%w: teams token", gwplat.ErrBadPayload)
	}
	t.token, t.expires = out.AccessToken, t.now().Add(time.Duration(out.ExpiresIn)*time.Second)
	return t.token, nil
}

// Send posts one rendered reply to the stored service URL.
func (t *Adapter) Send(ctx context.Context, chat, markdown string) error {
	t.mu.Lock()
	service := t.svc[chat]
	t.mu.Unlock()
	if service == "" {
		return fmt.Errorf("%w: service url for %s", ErrConfig, chat)
	}
	tok, err := t.accessToken(ctx)
	if err != nil {
		return fmt.Errorf("teams: %w", err)
	}
	err = t.out.Reply(ctx, gwplat.Reply{
		Chat: chat, Markdown: markdown,
		Send: func(ctx context.Context, part string) error {
			body := map[string]string{"type": "message", "text": part}
			_, derr := t.http.DoJSON(ctx, strings.TrimRight(service, "/")+"/v3/conversations/"+
				url.PathEscape(chat)+"/activities", body, map[string]string{"Authorization": "Bearer " + tok})
			if derr != nil {
				return fmt.Errorf("teams: %w", derr)
			}
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("teams: outbox: %w", err)
	}
	return nil
}

// Handler verifies the Bot Framework JWT and the tenant allowlist.
func (t *Adapter) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := t.http.ReadBody(w, r)
		if err != nil {
			return
		}
		if _, verr := gwplat.VerifyJWT(gwplat.Bearer(r), t.keys, t.issuer, t.appID, t.now()); verr != nil {
			gwplat.Deny(w, http.StatusUnauthorized)
			return
		}
		in, service, tenant, ok := parse(body)
		if !ok {
			gwplat.Deny(w, http.StatusBadRequest)
			return
		}
		if len(t.tenants) > 0 && !slicesContains(t.tenants, tenant) {
			gwplat.Deny(w, http.StatusForbidden)
			return
		}
		if err := validateEndpoint(service); err != nil {
			gwplat.Deny(w, http.StatusBadRequest)
			return
		}
		t.mu.Lock()
		t.svc[in.ChatID] = service
		t.mu.Unlock()
		if !t.dedupe.Claim("teams:"+in.UpdateID, t.now()) {
			w.WriteHeader(http.StatusOK)
			return
		}
		if derr := gwplat.Dispatch(r.Context(), t.handler, &in); derr != nil {
			gwplat.Deny(w, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func slicesContains(list []string, want string) bool { return slices.Contains(list, want) }

func validateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: service url", ErrConfig)
	}
	if u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == "localhost" || strings.HasPrefix(u.Hostname(), "127.")) {
		return nil
	}
	return fmt.Errorf("%w: https or loopback required", ErrConfig)
}

func parse(body []byte) (gateway.Inbound, string, string, bool) {
	var p struct {
		Type       string `json:"type"`
		ID         string `json:"id"`
		Text       string `json:"text"`
		ServiceURL string `json:"serviceUrl"`
		From       struct {
			ID string `json:"id"`
		} `json:"from"`
		Conversation struct {
			ID string `json:"id"`
		} `json:"conversation"`
		ChannelData struct {
			Tenant struct {
				ID string `json:"id"`
			} `json:"tenant"`
		} `json:"channelData"`
	}
	if json.Unmarshal(body, &p) != nil || p.Type != "message" || p.ID == "" || p.Conversation.ID == "" {
		return gateway.Inbound{}, "", "", false
	}
	return gateway.Inbound{
			Platform: "teams", ChatID: p.Conversation.ID, SenderID: p.From.ID,
			Text: p.Text, UpdateID: p.ID, Mentioned: true, Payload: string(body),
		},
		p.ServiceURL, p.ChannelData.Tenant.ID, true
}
