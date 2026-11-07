// Package weixin implements the PLAT-024 adapter: the iLink Bot API long-poll
// with a persisted cursor. Voice messages are not decoded here; MEDIA-031 is
// the audio decoder the pipeline uses.
package weixin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

// ErrConfig rejects an unusable activation.
var ErrConfig = errors.New("weixin: invalid config")

// CursorStore persists the iLink cursor across restarts.
type CursorStore interface {
	Load(ctx context.Context) (string, bool, error)
	Save(ctx context.Context, cursor string) error
}

// Config is the explicit activation of the adapter; the zero value is dormant.
type Config struct {
	Enabled    bool
	BaseURL    string
	Account    string
	HTTPClient *http.Client
	Backoff    gwplat.Backoff
	Cursor     CursorStore
	Log        *slog.Logger
	Now        func() time.Time
}

// Adapter long-polls one iLink account.
type Adapter struct {
	cfg     Config
	out     *gwplat.Outbox
	handler gwplat.Handler
	token   string
	cursor  string
	dedupe  *gwplat.Dedupe
	log     *slog.Logger
	now     func() time.Time
	client  *http.Client
}

// New validates the explicit config, loads the token from the secret seam, and
// restores the persisted cursor; it dials nothing.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource, out *gwplat.Outbox, h gwplat.Handler) (*Adapter, error) {
	if desc == nil || desc.Name != "weixin" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("weixin: activation: %w", err)
	}
	if err := validate(cfg, out, h); err != nil {
		return nil, err
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("weixin: secret: %w", err)
	}
	logger := cfg.Log
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	if cfg.HTTPClient == nil {
		return nil, fmt.Errorf("%w: HTTP client is required (E-05)", ErrConfig)
	}
	client := cfg.HTTPClient
	ad := &Adapter{
		cfg: *cfg, out: out, handler: h, token: values["weixin_token"],
		dedupe: gwplat.NewDedupe(8192, 30*time.Minute), log: logger, now: now, client: client,
	}
	if cfg.Cursor != nil {
		cursor, found, err := cfg.Cursor.Load(ctx)
		if err != nil {
			return nil, fmt.Errorf("weixin: cursor: %w", err)
		}
		if found {
			ad.cursor = cursor
		}
	}
	return ad, nil
}

func validate(cfg *Config, out *gwplat.Outbox, h gwplat.Handler) error {
	if err := validateBase(cfg.BaseURL); err != nil {
		return err
	}
	if _, ok := gwplat.SessionKey("weixin", cfg.Account); !ok {
		return fmt.Errorf("%w: account", ErrConfig)
	}
	if out == nil || h == nil {
		return fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	return nil
}

func validateBase(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("%w: base URL", ErrConfig)
	}
	if u.Scheme == "https" {
		return nil
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if u.Scheme == "http" && (strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("%w: base URL must be https or loopback http", ErrConfig)
}

// Run long-polls until ctx ends; transport failures back off.
func (a *Adapter) Run(ctx context.Context) error {
	backoff := gwplat.Backoff{}
	for attempt := 1; ; attempt++ {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		err := a.poll(ctx)
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		a.log.WarnContext(ctx, "weixin poll ended", "attempt", attempt, "err", err)
		if err == nil {
			continue
		}
		if waitErr := backoff.Wait(ctx, attempt); waitErr != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			return fmt.Errorf("weixin: reconnect: %w", waitErr)
		}
	}
}

type message struct {
	ID        string `json:"msg_id"`
	Chat      string `json:"chat_id"`
	From      string `json:"from"`
	Text      string `json:"text"`
	Type      string `json:"msg_type"`
	Group     bool   `json:"group"`
	Mentioned bool   `json:"mentioned"`
}

func (a *Adapter) poll(ctx context.Context) error {
	path := "/ilink/bot/getupdates?account=" + url.QueryEscape(a.cfg.Account) +
		"&cursor=" + url.QueryEscape(a.cursor) + "&timeout_s=" + strconv.Itoa(30)
	var batch struct {
		NextCursor string    `json:"next_cursor"`
		Messages   []message `json:"messages"`
	}
	if err := a.do(ctx, http.MethodGet, path, nil, &batch); err != nil {
		return err
	}
	for i := range batch.Messages {
		if err := a.dispatch(ctx, &batch.Messages[i]); err != nil {
			return err
		}
	}
	if batch.NextCursor != "" && batch.NextCursor != a.cursor {
		a.cursor = batch.NextCursor
		return a.saveCursor(ctx)
	}
	return nil
}

// saveCursor persists the new position before the next poll.
func (a *Adapter) saveCursor(ctx context.Context) error {
	if a.cfg.Cursor == nil {
		return nil
	}
	if err := a.cfg.Cursor.Save(ctx, a.cursor); err != nil {
		return fmt.Errorf("weixin: save cursor: %w", err)
	}
	return nil
}

func (a *Adapter) dispatch(ctx context.Context, msg *message) error {
	if msg.ID == "" || strings.TrimSpace(msg.Text) == "" || !a.dedupe.Claim("weixin:"+msg.ID, a.now()) {
		return nil
	}
	in := &gateway.Inbound{
		Platform: "weixin", Account: a.cfg.Account, ChatID: msg.Chat, SenderID: msg.From,
		UpdateID: "weixin:" + msg.ID, Text: msg.Text, Group: msg.Group, Mentioned: msg.Mentioned,
	}
	if err := gwplat.Dispatch(ctx, a.handler, in); err != nil {
		return fmt.Errorf("weixin: dispatch: %w", err)
	}
	return nil
}

// Send posts one rendered reply.
func (a *Adapter) Send(ctx context.Context, chat, markdown string) error {
	if err := a.out.Reply(ctx, gwplat.Reply{Chat: chat, Markdown: markdown, Send: a.sendText(chat)}); err != nil {
		return fmt.Errorf("weixin: reply: %w", err)
	}
	return nil
}

func (a *Adapter) sendText(chat string) gwplat.SendFunc {
	return func(ctx context.Context, text string) error {
		body := map[string]string{"account": a.cfg.Account, "chat": chat, "text": text}
		var out map[string]string
		return a.do(ctx, http.MethodPost, "/ilink/bot/send", body, &out)
	}
}

func (a *Adapter) do(ctx context.Context, method, path string, body, out any) error {
	var reader *strings.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("weixin: encode: %w", err)
		}
		reader = strings.NewReader(string(raw))
	} else {
		reader = strings.NewReader("")
	}
	endpoint := strings.TrimRight(a.cfg.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("weixin: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("weixin: transport: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			return
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("weixin: %s %s: status %d", method, path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("weixin: decode: %w", err)
	}
	return nil
}
