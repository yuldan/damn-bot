package simplex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

// Config is the explicit activation of the local simplex-chat daemon adapter.
type Config struct {
	Enabled    bool
	URL        string
	HTTPClient *http.Client
	Backoff    gwplat.Backoff
	Log        *slog.Logger
	Now        func() time.Time
}

// Adapter reads new chat items over the local WebSocket and sends commands.
type Adapter struct {
	cfg     Config
	desc    *gwplat.Descriptor
	out     *gwplat.Outbox
	handler gwplat.Handler
	dedupe  *gwplat.Dedupe
	log     *slog.Logger
	now     func() time.Time

	mu   sync.Mutex
	conn *websocket.Conn
}

// Errors of the adapter. Compared with errors.Is.
var (
	ErrConfig = errors.New("simplex: invalid config")
	ErrNoConn = errors.New("simplex: daemon is not connected")
)

// New validates an explicitly enabled, loopback-only daemon config.
func New(cfg *Config, desc *gwplat.Descriptor, out *gwplat.Outbox, h gwplat.Handler) (*Adapter, error) {
	if desc == nil || desc.Name != "simplex" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("simplex: activation: %w", err)
	}
	if err := validateDaemon(cfg); err != nil {
		return nil, err
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	logger := cfg.Log
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Adapter{
		cfg: *cfg, desc: desc, out: out, handler: h,
		dedupe: gwplat.NewDedupe(4096, 30*time.Minute),
		log:    logger, now: now,
	}, nil
}

// validateDaemon requires a ws:// loopback daemon URL.
func validateDaemon(cfg *Config) error {
	parsed, err := url.Parse(cfg.URL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Scheme != "ws" {
		return fmt.Errorf("%w: daemon URL", ErrConfig)
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("%w: daemon URL must be ws:// loopback", ErrConfig)
	}
	return nil
}

// Outbox exposes the shared outbound seam to the wiring.
func (a *Adapter) Outbox() *gwplat.Outbox { return a.out }

// Run holds the daemon WebSocket, reconnecting with capped backoff.
func (a *Adapter) Run(ctx context.Context) error {
	for attempt := 1; ; attempt++ {
		err := a.session(ctx)
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		a.log.WarnContext(ctx, "simplex session ended", "attempt", attempt, "err", err)
		if waitErr := a.cfg.Backoff.Wait(ctx, attempt); waitErr != nil {
			return fmt.Errorf("simplex: reconnect: %w", waitErr)
		}
	}
}

func (a *Adapter) session(ctx context.Context) error {
	conn, resp, err := websocket.Dial(ctx, a.cfg.URL, &websocket.DialOptions{HTTPClient: a.cfg.HTTPClient})
	closeBody(resp)
	if err != nil {
		return fmt.Errorf("simplex: dial: %w", err)
	}
	defer func() {
		if cerr := conn.CloseNow(); cerr != nil {
			return
		}
	}()
	a.setConn(conn)
	defer a.setConn(nil)
	if err := a.write(ctx, `{"corrId":"damnbot-sub","cmd":"/subscribe"}`); err != nil {
		return err
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("simplex: read: %w", err)
		}
		a.handleFrame(ctx, data)
	}
}

type frame struct {
	CorrID string `json:"corrId"`
	Resp   *struct {
		Type     string `json:"type"`
		ChatItem *struct {
			ChatInfo struct {
				Contact *struct {
					ContactID string `json:"contactId"`
				} `json:"contact"`
				GroupInfo *struct {
					GroupID string `json:"groupId"`
				} `json:"groupInfo"`
			} `json:"chatInfo"`
			Meta struct {
				ItemID string `json:"itemId"`
			} `json:"meta"`
			Content struct {
				MsgContent *struct {
					Text      string `json:"text"`
					Mentioned bool   `json:"mentioned"`
				} `json:"msgContent"`
			} `json:"content"`
		} `json:"chatItem"`
	} `json:"resp"`
}

func (a *Adapter) handleFrame(ctx context.Context, data []byte) {
	var f frame
	if err := json.Unmarshal(data, &f); err != nil || f.Resp == nil || f.Resp.Type != "newChatItem" || f.Resp.ChatItem == nil {
		return
	}
	item := f.Resp.ChatItem
	if item.Meta.ItemID == "" || item.Content.MsgContent == nil || item.Content.MsgContent.Text == "" {
		return
	}
	chat, group := "", false
	if item.ChatInfo.GroupInfo != nil && item.ChatInfo.GroupInfo.GroupID != "" {
		chat, group = item.ChatInfo.GroupInfo.GroupID, true
	} else if item.ChatInfo.Contact != nil {
		chat = item.ChatInfo.Contact.ContactID
	}
	if chat == "" || !a.dedupe.Claim("simplex:"+item.Meta.ItemID, a.now()) {
		return
	}
	in := &gateway.Inbound{
		Platform:  "simplex",
		Account:   "simplex",
		ChatID:    chat,
		SenderID:  chat,
		UpdateID:  "simplex:" + item.Meta.ItemID,
		Text:      item.Content.MsgContent.Text,
		Group:     group,
		Mentioned: item.Content.MsgContent.Mentioned,
	}
	if err := gwplat.Dispatch(ctx, a.handler, in); err != nil {
		a.log.WarnContext(ctx, "simplex: dispatch failed", "err", err)
	}
}

// Publish sends one already-rendered reply as a daemon command.
func (a *Adapter) Publish(ctx context.Context, chat, text string) error {
	payload, err := json.Marshal(map[string]string{"corrId": "damnbot-send", "cmd": "@" + chat + " " + text})
	if err != nil {
		return fmt.Errorf("simplex: payload: %w", err)
	}
	return a.write(ctx, string(payload))
}

func (a *Adapter) write(ctx context.Context, payload string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.conn == nil {
		return ErrNoConn
	}
	if err := a.conn.Write(ctx, websocket.MessageText, []byte(payload)); err != nil {
		return fmt.Errorf("simplex: write: %w", err)
	}
	return nil
}

func (a *Adapter) setConn(conn *websocket.Conn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.conn = conn
}

func closeBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	if err := resp.Body.Close(); err != nil {
		return
	}
}
