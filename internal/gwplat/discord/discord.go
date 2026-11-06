// Package discord implements the PLAT-005 adapter: Gateway WebSocket, minimal
// intents, thread/DM mapping, slash commands and REST sends through the shared
// outbox; a permanent close (4004) stops and reports needs_attention (GW-11).
package discord

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
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

// The only credential the adapter reads.
// Gateway intents. MESSAGE_CONTENT is privileged and therefore opt-in.
const (
	IntentGuilds         = 1 << 0
	IntentGuildMessages  = 1 << 9
	IntentDirectMessages = 1 << 12
	IntentMessageContent = 1 << 15

	minimalIntents = IntentGuilds | IntentGuildMessages | IntentDirectMessages
)

// Permanent gateway close codes: retrying cannot fix them.
const (
	closeUnknownOpcode    = 4001
	closeNotAuthenticated = 4003
	closeAuthFailed       = 4004
	closeInvalidIntents   = 4013
	closeDisallowedIntent = 4014
)

// Gateway opcodes.
const (
	opDispatch       = 0
	opHeartbeat      = 1
	opIdentify       = 2
	opReconnect      = 7
	opInvalidSession = 9
	opHello          = 10
)

// Config is the explicit activation of the adapter; the zero value dials nothing.
type Config struct {
	Enabled    bool
	BaseURL    string
	GatewayURL string
	HTTPClient *http.Client
	// Intents adds to the minimal set; MessageContent opts into the privileged
	// content intent (never implicit).
	Intents        int
	MessageContent bool
	Backoff        gwplat.Backoff
	Log            *slog.Logger
	Now            func() time.Time
}

// Adapter is one Discord bot connection.
type Adapter struct {
	cfg     Config
	out     *gwplat.Outbox
	handler gwplat.Handler
	token   string
	dedupe  *gwplat.Dedupe
	log     *slog.Logger
	now     func() time.Time
	client  *http.Client

	mu        sync.Mutex
	conn      *websocket.Conn
	botUserID string
	seq       atomic.Int64
	channels  map[string]channelInfo
}

type channelInfo struct {
	Type   int    `json:"type"`
	Parent string `json:"parent_id"`
}

// Errors of the adapter. Compared with errors.Is.
var (
	ErrConfig         = errors.New("discord: invalid config")
	ErrNeedsAttention = errors.New("discord: needs owner attention")
)

// AttentionError is a permanent gateway failure (GW-11), unwrapping to both the
// needs_attention verdict and the module_unavailable reason.
type AttentionError struct {
	Code   int
	Reason string
}

// Error implements error.
func (e *AttentionError) Error() string {
	return fmt.Sprintf("module_unavailable{discord_%s} (needs_attention, close %d)", e.Reason, e.Code)
}

// Unwrap exposes the verdict and the module reason.
func (e *AttentionError) Unwrap() []error {
	return []error{ErrNeedsAttention, gwplat.Unavailablef("discord", "discord_%s", e.Reason)}
}

func attention(code int, reason string) error { return &AttentionError{Code: code, Reason: reason} }

// New validates the explicit config, loads the bot token from the secret seam,
// and returns a dormant adapter; it opens no connection.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource, out *gwplat.Outbox, h gwplat.Handler) (*Adapter, error) {
	if desc == nil || desc.Name != "discord" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("discord: activation: %w", err)
	}
	if err := validateEndpoints(cfg); err != nil {
		return nil, err
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("discord: secret: %w", err)
	}
	token := strings.TrimSpace(values["discord_bot_token"])
	if token == "" || strings.ContainsAny(token, " \r\n") {
		return nil, fmt.Errorf("%w: %w", ErrConfig, gwplat.Unavailablef("discord", "discord_missing_secret"))
	}
	logger := cfg.Log
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &Adapter{
		cfg: *cfg, out: out, handler: h, token: token,
		dedupe: gwplat.NewDedupe(4096, 30*time.Minute), log: logger, now: now, client: client,
		channels: make(map[string]channelInfo),
	}, nil
}

// validateEndpoints keeps endpoints on TLS or plaintext loopback only.
func validateEndpoints(cfg *Config) error {
	if cfg.Intents < 0 {
		return fmt.Errorf("%w: negative intents", ErrConfig)
	}
	check := func(raw, scheme, plain, label string) error {
		if raw == "" {
			return nil
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil {
			return fmt.Errorf("%w: %s", ErrConfig, label)
		}
		if u.Scheme != scheme && (u.Scheme != plain || !isLoopback(u.Hostname())) {
			return fmt.Errorf("%w: %s must be %s or loopback %s", ErrConfig, label, scheme, plain)
		}
		return nil
	}
	if err := check(cfg.BaseURL, "https", "http", "base URL"); err != nil {
		return err
	}
	return check(cfg.GatewayURL, "wss", "ws", "gateway URL")
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Intents is the exact bitmask the adapter identifies with.
func (a *Adapter) Intents() int {
	intents := minimalIntents | (a.cfg.Intents &^ minimalIntents)
	if a.cfg.MessageContent {
		intents |= IntentMessageContent
	}
	return intents
}

// Outbox exposes the shared outbound seam to the wiring.
func (a *Adapter) Outbox() *gwplat.Outbox { return a.out }

func (a *Adapter) baseURL() string {
	if a.cfg.BaseURL != "" {
		return strings.TrimRight(a.cfg.BaseURL, "/")
	}
	return "https://discord.com/api/v10"
}

func (a *Adapter) gatewayURL() string {
	base := a.cfg.GatewayURL
	if base == "" {
		base = "wss://gateway.discord.gg"
	}
	return strings.TrimRight(base, "/") + "/?v=10&encoding=json"
}

// Run holds the gateway connection; a permanent close returns needs_attention.
func (a *Adapter) Run(ctx context.Context) error {
	for attempt := 1; ; attempt++ {
		err := a.session(ctx)
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if errors.Is(err, ErrNeedsAttention) {
			return err
		}
		a.log.WarnContext(ctx, "discord gateway session ended", "attempt", attempt, "err", err)
		if waitErr := a.cfg.Backoff.Wait(ctx, attempt); waitErr != nil {
			return fmt.Errorf("discord: reconnect: %w", waitErr)
		}
	}
}

func (a *Adapter) session(ctx context.Context) error {
	conn, resp, err := websocket.Dial(ctx, a.gatewayURL(), &websocket.DialOptions{HTTPClient: a.client})
	if resp != nil && resp.Body != nil {
		defer func() {
			if cerr := resp.Body.Close(); cerr != nil {
				return
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("discord: gateway dial: %w", err)
	}
	defer func() {
		if cerr := conn.CloseNow(); cerr != nil {
			return
		}
	}()
	a.setConn(conn)
	defer a.setConn(nil)
	hbCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return classifyClose(err)
		}
		if err := a.handleFrame(hbCtx, data); err != nil {
			return err
		}
	}
}

// permanentReason names the needs_attention reason of a permanent close code.
func permanentReason(code websocket.StatusCode) (string, bool) {
	reason, ok := map[websocket.StatusCode]string{
		closeAuthFailed:       "auth_failed",
		closeNotAuthenticated: "not_authenticated",
		closeDisallowedIntent: "disallowed_intents",
		closeInvalidIntents:   "invalid_intents",
		closeUnknownOpcode:    "unknown_opcode",
	}[code]
	return reason, ok
}

// classifyClose maps a websocket close to the permanent or transient class.
func classifyClose(err error) error {
	code := websocket.CloseStatus(err)
	if reason, ok := permanentReason(code); ok {
		return attention(int(code), reason)
	}
	return fmt.Errorf("discord: gateway read: %w", err)
}

func (a *Adapter) handleFrame(ctx context.Context, data []byte) error {
	frame, ok := parseGatewayFrame(data)
	if !ok {
		a.log.WarnContext(ctx, "discord: malformed frame")
		return nil
	}
	{
		if frame.S > a.seq.Load() {
			a.seq.Store(frame.S)
		}
		switch frame.Op {
		case opHello:
			var hello struct {
				HeartbeatInterval int `json:"heartbeat_interval"`
			}
			if err := json.Unmarshal(frame.D, &hello); err != nil {
				return fmt.Errorf("discord: hello payload: %w", err)
			}
			if hello.HeartbeatInterval <= 0 {
				return fmt.Errorf("discord: hello without a heartbeat interval")
			}
			if err := a.write(ctx, map[string]any{
				"op": opIdentify,
				"d": map[string]any{
					"token":   a.token,
					"intents": a.Intents(),
					"properties": map[string]string{
						"os": "linux", "browser": "damnbot", "device": "damnbot",
					},
				},
			}); err != nil {
				return err
			}
			go a.heartbeatLoop(ctx, time.Duration(hello.HeartbeatInterval)*time.Millisecond)
		case opReconnect, opInvalidSession:
			return fmt.Errorf("discord: gateway asked for a new session (op %d)", frame.Op)
		case opDispatch:
			a.dispatch(ctx, frame.T, frame.D)
		}
	}
	return nil
}

// parseGatewayFrame decodes one gateway frame; malformed frames are dropped.
func parseGatewayFrame(data []byte) (gatewayFrame, bool) {
	var frame gatewayFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return gatewayFrame{}, false
	}
	return frame, true
}

func (a *Adapter) heartbeatLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.write(ctx, map[string]any{"op": opHeartbeat, "d": a.seq.Load()}); err != nil {
				return
			}
		}
	}
}

func (a *Adapter) write(ctx context.Context, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("discord: encode: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.conn == nil {
		return fmt.Errorf("%w: gateway is not connected", ErrConfig)
	}
	if err := a.conn.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("discord: gateway write: %w", err)
	}
	return nil
}

func (a *Adapter) setConn(conn *websocket.Conn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.conn = conn
}

// dispatch maps one gateway event to an inbound message.
func (a *Adapter) dispatch(ctx context.Context, typ string, data json.RawMessage) {
	var in *gateway.Inbound
	switch typ {
	case "READY":
		var ready struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		}
		if json.Unmarshal(data, &ready) != nil {
			return
		}
		a.mu.Lock()
		a.botUserID = ready.User.ID
		a.mu.Unlock()
		return
	case "GUILD_CREATE":
		var guild struct {
			Channels []json.RawMessage `json:"channels"`
		}
		if json.Unmarshal(data, &guild) != nil {
			return
		}
		a.cacheChannels(guild.Channels)
		return
	case "CHANNEL_CREATE":
		a.cacheChannels([]json.RawMessage{data})
		return
	case "MESSAGE_CREATE":
		in = a.messageInbound(data)
	case "INTERACTION_CREATE":
		in = a.interactionInbound(data)
	}
	if in == nil {
		return
	}
	if err := gwplat.Dispatch(ctx, a.handler, in); err != nil {
		a.log.WarnContext(ctx, "discord: dispatch failed", "err", err)
	}
}

func (a *Adapter) cacheChannels(raws []json.RawMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, raw := range raws {
		var ch struct {
			ID string `json:"id"`
			channelInfo
		}
		if err := json.Unmarshal(raw, &ch); err != nil || ch.ID == "" {
			continue
		}
		a.channels[ch.ID] = ch.channelInfo
	}
}

func (a *Adapter) lookupChannel(id string) channelInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.channels[id]
}

func (a *Adapter) botID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.botUserID
}

// messageInbound maps MESSAGE_CREATE; a thread keeps the parent channel as the
// session chat and the thread channel as ThreadID.
func (a *Adapter) messageInbound(data json.RawMessage) *gateway.Inbound {
	var msg struct {
		ID        string `json:"id"`
		ChannelID string `json:"channel_id"`
		GuildID   string `json:"guild_id"`
		Content   string `json:"content"`
		Author    struct {
			ID  string `json:"id"`
			Bot bool   `json:"bot"`
		} `json:"author"`
		Mentions []struct {
			ID string `json:"id"`
		} `json:"mentions"`
	}
	if err := json.Unmarshal(data, &msg); err != nil || msg.ID == "" || msg.ChannelID == "" {
		return nil
	}
	if msg.Author.Bot || msg.Author.ID == "" || msg.Author.ID == a.botID() {
		return nil
	}
	if !a.dedupe.Claim("discord:"+msg.ID, a.now()) {
		return nil
	}
	info := a.lookupChannel(msg.ChannelID)
	chat, thread := msg.ChannelID, ""
	if info.Type >= 10 && info.Type <= 12 { // thread channels
		chat, thread = info.Parent, msg.ChannelID
	}
	group := msg.GuildID != ""
	mentioned := false
	for _, m := range msg.Mentions {
		if m.ID != "" && m.ID == a.botID() {
			mentioned = true
		}
	}
	return &gateway.Inbound{
		Platform: "discord", Account: a.botID(), ChatID: chat, ThreadID: thread,
		SenderID: msg.Author.ID, UpdateID: "discord:" + msg.ID, Text: msg.Content,
		Group: group, Mentioned: mentioned,
	}
}

// interactionInbound maps a slash-command interaction (type 2).
func (a *Adapter) interactionInbound(data json.RawMessage) *gateway.Inbound {
	var it struct {
		ID        string `json:"id"`
		Type      int    `json:"type"`
		ChannelID string `json:"channel_id"`
		GuildID   string `json:"guild_id"`
		Member    struct {
			User struct{ ID string } `json:"user"`
		} `json:"member"`
		User struct{ ID string } `json:"user"`
		Data struct {
			Name    string `json:"name"`
			Options []struct {
				Value json.RawMessage `json:"value"`
			} `json:"options"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &it); err != nil || it.ID == "" || it.Type != 2 || it.Data.Name == "" {
		return nil
	}
	if !a.dedupe.Claim("discord:"+it.ID, a.now()) {
		return nil
	}
	sender := it.Member.User.ID
	if sender == "" {
		sender = it.User.ID
	}
	if sender == "" {
		return nil
	}
	var text strings.Builder
	text.WriteString("/")
	text.WriteString(it.Data.Name)
	for _, opt := range it.Data.Options {
		var value string
		if json.Unmarshal(opt.Value, &value) == nil && value != "" {
			text.WriteString(" ")
			text.WriteString(value)
		}
	}
	return &gateway.Inbound{
		Platform: "discord", Account: a.botID(), ChatID: it.ChannelID,
		SenderID: sender, UpdateID: "discord:" + it.ID, Text: text.String(),
		Group: it.GuildID != "", Mentioned: true, Command: true,
	}
}

// Send renders and queues one markdown reply over REST (a thread is a channel).
func (a *Adapter) Send(ctx context.Context, chat, markdown string) error {
	if chat == "" {
		return fmt.Errorf("%w: empty chat", ErrConfig)
	}
	endpoint := a.baseURL() + "/channels/" + url.PathEscape(chat) + "/messages"
	send := func(ctx context.Context, text string) error {
		body, err := json.Marshal(map[string]any{"content": text, "allowed_mentions": map[string]any{"parse": []string{}}})
		if err != nil {
			return fmt.Errorf("discord: encode: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
		if err != nil {
			return fmt.Errorf("discord: request: %w", err)
		}
		req.Header.Set("Authorization", "Bot "+a.token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := a.client.Do(req)
		if err != nil {
			return fmt.Errorf("discord: send: %w", err)
		}
		defer func() {
			if cerr := resp.Body.Close(); cerr != nil {
				return
			}
		}()
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode <= 299:
			return nil
		case resp.StatusCode == http.StatusUnauthorized:
			return attention(closeAuthFailed, "auth_failed")
		default:
			return fmt.Errorf("discord: send status %d", resp.StatusCode)
		}
	}
	if err := a.out.Reply(ctx, gwplat.Reply{Chat: chat, Markdown: markdown, Send: send}); err != nil {
		return fmt.Errorf("discord: reply: %w", err)
	}
	return nil
}

type gatewayFrame struct {
	Op int             `json:"op"`
	T  string          `json:"t"`
	S  int64           `json:"s"`
	D  json.RawMessage `json:"d"`
}
