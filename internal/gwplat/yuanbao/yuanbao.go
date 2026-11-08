// Package yuanbao implements PLAT-027: the Tencent Yuanbao IM bot over a
// WebSocket. The handshake carries the app id and the app secret resolved
// from the gwplat secret seam; group and DM frames map to gateway.Inbound,
// replays are deduped per event id, and every reply goes through the shared
// gwplat.Outbox (render → outq).
package yuanbao

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

// Adapter errors. Compared with errors.Is.
var (
	ErrConfig         = errors.New("yuanbao: invalid config")
	ErrAuth           = errors.New("yuanbao: handshake rejected")
	ErrClosed         = errors.New("yuanbao: not connected")
	ErrDMNotAllowed   = errors.New("yuanbao: DM identity is not allowlisted")
	ErrGroupUnknown   = errors.New("yuanbao: unknown group")
	ErrStickerUnknown = errors.New("yuanbao: unknown sticker")
	ErrBadFrame       = errors.New("yuanbao: invalid frame")
)

// Config is the explicit activation of the adapter. The zero value dials
// nothing.
type Config struct {
	Enabled     bool
	WSURL       string
	AppID       string
	SecretName  string
	Secrets     gwplat.SecretSource
	HTTP        *http.Client
	Backoff     gwplat.Backoff
	Log         *slog.Logger
	Now         func() time.Time
	DMAllowlist []string
	Stickers    []Sticker
}

// Sticker is one sticker the operator allows this bot to send.
type Sticker struct {
	ID       string   `json:"id"`
	Keywords []string `json:"keywords,omitempty"`
}

// Member is one cached group member.
type Member struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Group is the cached metadata of one group conversation.
type Group struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Members []Member `json:"members,omitempty"`
}

// Frame is the Yuanbao bot frame (both directions). It is deliberately small:
// only text messages and sticker actions exist in this adapter.
type Frame struct {
	Type       string   `json:"type"`
	EventID    string   `json:"event_id,omitempty"`
	ChatType   string   `json:"chat_type,omitempty"`
	ChatID     string   `json:"chat_id,omitempty"`
	ChatName   string   `json:"chat_name,omitempty"`
	SenderID   string   `json:"sender_id,omitempty"`
	SenderName string   `json:"sender_name,omitempty"`
	Text       string   `json:"text,omitempty"`
	Mentioned  bool     `json:"mentioned,omitempty"`
	Members    []Member `json:"members,omitempty"`
	StickerID  string   `json:"sticker_id,omitempty"`
	AppID      string   `json:"app_id,omitempty"`
	AppSecret  string   `json:"app_secret,omitempty"`
}

// Adapter is one configured Yuanbao connection.
type Adapter struct {
	cfg      Config
	desc     *gwplat.Descriptor
	out      *gwplat.Outbox
	handler  gwplat.Handler
	dedupe   *gwplat.Dedupe
	log      *slog.Logger
	now      func() time.Time
	client   *http.Client
	secret   string
	dms      map[string]bool
	stickers map[string]Sticker

	writeMu sync.Mutex
	mu      sync.Mutex
	conn    *websocket.Conn
	groups  map[string]Group
}

// New validates and resolves the secret, then returns a dormant adapter.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, out *gwplat.Outbox, h gwplat.Handler) (*Adapter, error) {
	if cfg == nil || desc == nil || desc.Name != "yuanbao" {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("yuanbao: activation: %w", err)
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	name := cfg.SecretName
	if name == "" {
		name = desc.Secrets[0]
	}
	decl := *desc
	decl.Secrets = []string{name}
	values, err := gwplat.ResolveSecrets(ctx, &decl, cfg.Secrets)
	if err != nil {
		return nil, fmt.Errorf("yuanbao: app secret: %w", err)
	}
	secret := values[name]
	if secret == "" {
		return nil, fmt.Errorf("yuanbao: activation: %w", gwplat.Unavailablef("yuanbao", "yuanbao_missing_secret"))
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
	client := cfg.HTTP
	if client == nil {
		client = netx.LoopbackClient()
	}
	a := &Adapter{
		cfg: *cfg, desc: desc, out: out, handler: h,
		dedupe: gwplat.NewDedupe(4096, 30*time.Minute),
		log:    logger, now: now, client: client, secret: secret,
		dms:      make(map[string]bool, len(cfg.DMAllowlist)),
		stickers: make(map[string]Sticker, len(cfg.Stickers)),
		groups:   make(map[string]Group),
	}
	if err := a.loadAllowlists(cfg); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Adapter) loadAllowlists(cfg *Config) error {
	for _, id := range cfg.DMAllowlist {
		if id == "" || strings.ContainsAny(id, " \x00\r\n") {
			return fmt.Errorf("%w: DM allowlist", ErrConfig)
		}
		a.dms[id] = true
	}
	for _, sticker := range cfg.Stickers {
		if sticker.ID == "" {
			return fmt.Errorf("%w: sticker id", ErrConfig)
		}
		a.stickers[sticker.ID] = sticker
	}
	return nil
}

func validateConfig(cfg *Config) error {
	u, err := url.Parse(cfg.WSURL)
	if err != nil || cfg.AppID == "" {
		return fmt.Errorf("%w: url/app id", ErrConfig)
	}
	switch u.Scheme {
	case "wss":
	case "ws":
		if !loopbackHost(u.Hostname()) {
			return fmt.Errorf("%w: ws is loopback-only", ErrConfig)
		}
	default:
		return fmt.Errorf("%w: scheme %q", ErrConfig, u.Scheme)
	}
	if u.Host == "" || u.User != nil || strings.ContainsAny(cfg.AppID, " \x00\r\n") {
		return fmt.Errorf("%w: url/app id", ErrConfig)
	}
	return nil
}

func closeBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	if err := resp.Body.Close(); err != nil {
		return
	}
}

func loopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

// Run reconnects with capped backoff until ctx ends.
func (a *Adapter) Run(ctx context.Context) error {
	for attempt := 1; ; attempt++ {
		err := a.session(ctx)
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		a.log.WarnContext(ctx, "yuanbao connection ended", "attempt", attempt, "err", err)
		if errors.Is(err, ErrAuth) && attempt >= 3 {
			return fmt.Errorf("yuanbao: handshake: %w", err)
		}
		if waitErr := a.cfg.Backoff.Wait(ctx, attempt); waitErr != nil {
			return fmt.Errorf("yuanbao: reconnect: %w", waitErr)
		}
	}
}

func (a *Adapter) session(ctx context.Context) error {
	conn, resp, err := websocket.Dial(ctx, a.cfg.WSURL, &websocket.DialOptions{HTTPClient: a.client})
	closeBody(resp)
	if err != nil {
		return fmt.Errorf("yuanbao: dial: %w", err)
	}
	a.mu.Lock()
	a.conn = conn
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.conn == conn {
			a.conn = nil
		}
		a.mu.Unlock()
		if cerr := conn.CloseNow(); cerr != nil {
			return
		}
	}()
	if err := a.handshake(ctx, conn); err != nil {
		return err
	}
	for {
		_, raw, rerr := conn.Read(ctx)
		if rerr != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("yuanbao: read: %w", ctx.Err())
			}
			return fmt.Errorf("yuanbao: read: %w", rerr)
		}
		a.handleFrame(ctx, raw)
	}
}

func (a *Adapter) handshake(ctx context.Context, conn *websocket.Conn) error {
	auth, err := json.Marshal(Frame{Type: "auth", AppID: a.cfg.AppID, AppSecret: a.secret})
	if err != nil {
		return fmt.Errorf("yuanbao: auth frame: %w", err)
	}
	if werr := conn.Write(ctx, websocket.MessageText, auth); werr != nil {
		return fmt.Errorf("yuanbao: auth write: %w", werr)
	}
	_, raw, err := conn.Read(ctx)
	if err != nil {
		return fmt.Errorf("yuanbao: auth read: %w", err)
	}
	var frame Frame
	if err := json.Unmarshal(raw, &frame); err != nil || frame.Type != "auth_ok" {
		return fmt.Errorf("%w: %s", ErrAuth, "yuanbao_auth_failed")
	}
	return nil
}

// handleFrame maps one inbound frame. A replay of the same event id never
// becomes a second turn.
func (a *Adapter) handleFrame(ctx context.Context, raw []byte) {
	var frame Frame
	if err := json.Unmarshal(raw, &frame); err != nil || frame.Type != "message" {
		return
	}
	if frame.EventID == "" || frame.ChatID == "" || frame.SenderID == "" || frame.Text == "" {
		a.log.WarnContext(ctx, "yuanbao: dropped malformed message")
		return
	}
	if !a.dedupe.Claim("yuanbao:"+frame.EventID, a.now()) {
		return
	}
	group := frame.ChatType == "group"
	a.rememberGroup(&frame, group)
	in := &gateway.Inbound{
		Platform: "yuanbao", Account: a.cfg.AppID,
		ChatID: frame.ChatID, SenderID: frame.SenderID,
		UpdateID: "yuanbao:" + frame.EventID, Text: frame.Text,
		Group: group, Mentioned: frame.Mentioned, ReplyToBot: false,
	}
	if err := gwplat.Dispatch(ctx, a.handler, in); err != nil {
		a.log.WarnContext(ctx, "yuanbao: dispatch failed", "err", err)
	}
}

func (a *Adapter) rememberGroup(frame *Frame, group bool) {
	if !group {
		return
	}
	g := Group{ID: frame.ChatID, Name: frame.ChatName, Members: frame.Members}
	a.mu.Lock()
	a.groups[frame.ChatID] = g
	a.mu.Unlock()
}

// Reply renders markdown through the descriptor's renderer and queues it in
// the shared outq scheduler.
func (a *Adapter) Reply(ctx context.Context, chat string, group bool, markdown string) error {
	err := a.out.Reply(ctx, gwplat.Reply{
		Chat: chat, Group: group, Markdown: markdown,
		Send: func(ctx context.Context, text string) error {
			return a.send(ctx, &Frame{Type: "send", ChatID: chat, Text: text})
		},
	})
	if err != nil {
		return fmt.Errorf("yuanbao: reply: %w", err)
	}
	return nil
}

// SendDM sends one text to an allowlisted user.
func (a *Adapter) SendDM(ctx context.Context, user, text string) error {
	if !a.dms[user] {
		return fmt.Errorf("%w: %s", ErrDMNotAllowed, user)
	}
	return a.send(ctx, &Frame{Type: "send", ChatID: "dm:" + user, Text: text})
}

// SendSticker sends one catalogued sticker to a chat.
func (a *Adapter) SendSticker(ctx context.Context, chat, sticker string) error {
	if _, ok := a.stickers[sticker]; !ok {
		return fmt.Errorf("%w: %s", ErrStickerUnknown, sticker)
	}
	return a.send(ctx, &Frame{Type: "sticker", ChatID: chat, StickerID: sticker})
}

// GroupInfo returns the cached group metadata.
func (a *Adapter) GroupInfo(group string) (Group, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	g, ok := a.groups[group]
	if !ok {
		return Group{}, fmt.Errorf("%w: %s", ErrGroupUnknown, group)
	}
	return g, nil
}

// GroupMembers returns the cached member list of a group.
func (a *Adapter) GroupMembers(group string) ([]Member, error) {
	g, err := a.GroupInfo(group)
	if err != nil {
		return nil, err
	}
	return append([]Member(nil), g.Members...), nil
}

// SearchSticker returns the catalogue entries matching query; an empty query
// returns every sticker.
func (a *Adapter) SearchSticker(query string) []Sticker {
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]Sticker, 0, len(a.stickers))
	for _, s := range a.stickers {
		if q == "" || strings.Contains(strings.ToLower(s.ID), q) || matchKeywords(s, q) {
			out = append(out, s)
		}
	}
	return out
}

func matchKeywords(s Sticker, q string) bool {
	for _, kw := range s.Keywords {
		if strings.Contains(strings.ToLower(kw), q) {
			return true
		}
	}
	return false
}

func (a *Adapter) send(ctx context.Context, frame *Frame) error {
	a.mu.Lock()
	conn := a.conn
	a.mu.Unlock()
	if conn == nil {
		return ErrClosed
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("yuanbao: encode: %w", err)
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
		return fmt.Errorf("yuanbao: write: %w", err)
	}
	return nil
}
