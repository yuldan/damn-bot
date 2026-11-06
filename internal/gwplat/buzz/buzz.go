package buzz

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

// Config is the explicit activation of the Buzz relay adapter. The zero value
// is disabled and dials nothing.
type Config struct {
	Enabled    bool
	RelayURL   string
	HTTPClient *http.Client
	Backoff    gwplat.Backoff
	Log        *slog.Logger
	Now        func() time.Time
	Rand       io.Reader
}

// Adapter is the Buzz (Nostr relay) adapter: one relay subscription carrying
// mentions and channel notes, and one publish path for replies.
type Adapter struct {
	cfg     Config
	desc    *gwplat.Descriptor
	out     *gwplat.Outbox
	handler gwplat.Handler
	priv    [32]byte
	pub     string
	dedupe  *gwplat.Dedupe
	log     *slog.Logger
	now     func() time.Time
	rand    io.Reader

	mu   sync.Mutex
	conn *websocket.Conn
}

// Errors of the adapter. Compared with errors.Is.
var (
	ErrConfig = errors.New("buzz: invalid config")
	ErrNoConn = errors.New("buzz: relay is not connected")
)

// New validates explicitly enabled config, loads the private key only from the
// secret seam, and returns a dormant adapter. It opens no connection.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource, out *gwplat.Outbox, h gwplat.Handler) (*Adapter, error) {
	if desc == nil || desc.Name != "buzz" {
		return nil, fmt.Errorf("%w: descriptor is not buzz", ErrConfig)
	}
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("buzz: activation: %w", err)
	}
	if err := validateRelayURL(cfg.RelayURL); err != nil {
		return nil, err
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("buzz: secret: %w", err)
	}
	priv, err := parseKey(values["buzz_private_key"])
	if err != nil {
		return nil, err
	}
	pubKey := publicKey(priv)
	logger := cfg.Log
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	random := cfg.Rand
	if random == nil {
		random = rand.Reader
	}
	return &Adapter{
		cfg:     *cfg,
		desc:    desc,
		out:     out,
		handler: h,
		priv:    priv,
		pub:     hex.EncodeToString(pubKey[:]),
		dedupe:  gwplat.NewDedupe(4096, 30*time.Minute),
		log:     logger,
		now:     now,
		rand:    random,
	}, nil
}

func validateRelayURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: relay URL", ErrConfig)
	}
	if u.User != nil {
		return fmt.Errorf("%w: relay URL must not carry credentials", ErrConfig)
	}
	switch u.Scheme {
	case "wss":
		return nil
	case "ws":
		if !isLoopback(u.Hostname()) {
			return fmt.Errorf("%w: plain ws:// is only allowed on loopback", ErrConfig)
		}
		return nil
	default:
		return fmt.Errorf("%w: relay URL scheme %q", ErrConfig, u.Scheme)
	}
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Run holds one relay subscription, reconnecting with capped backoff after a
// disconnect. It returns nil when ctx ends.
func (a *Adapter) Run(ctx context.Context) error {
	for attempt := 1; ; attempt++ {
		err := a.session(ctx)
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		a.log.WarnContext(ctx, "buzz relay session ended", "attempt", attempt, "err", err)
		if waitErr := a.cfg.Backoff.Wait(ctx, attempt); waitErr != nil {
			return fmt.Errorf("buzz: reconnect: %w", waitErr)
		}
	}
}

func (a *Adapter) session(ctx context.Context) error {
	conn, resp, err := websocket.Dial(ctx, a.cfg.RelayURL, &websocket.DialOptions{HTTPClient: a.cfg.HTTPClient})
	closeResponse(resp)
	if err != nil {
		return fmt.Errorf("buzz: relay dial: %w", err)
	}
	defer func() {
		if cerr := conn.CloseNow(); cerr != nil {
			return
		}
	}()
	a.setConn(conn)
	defer a.setConn(nil)
	req, err := json.Marshal([]any{"REQ", "damnbot", map[string]any{"kinds": []int{kindNote, kindChannel}, "#p": []string{a.pub}}})
	if err != nil {
		return fmt.Errorf("buzz: subscription: %w", err)
	}
	if err := a.write(ctx, req); err != nil {
		return err
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("buzz: relay read: %w", err)
		}
		a.handleFrame(ctx, data)
	}
}

// handleFrame decodes one relay frame; malformed frames are dropped instead of
// ending the subscription.
func (a *Adapter) handleFrame(ctx context.Context, data []byte) {
	ev, ok := decodeFrame(data)
	if !ok {
		return
	}
	if err := a.ingest(ctx, &ev); err != nil {
		a.log.WarnContext(ctx, "buzz: dispatch failed", "err", err)
	}
}

func decodeFrame(data []byte) (event, bool) {
	var frame []json.RawMessage
	if err := json.Unmarshal(data, &frame); err != nil || len(frame) < 2 {
		return event{}, false
	}
	var kind string
	if err := json.Unmarshal(frame[0], &kind); err != nil || kind != "EVENT" {
		return event{}, false
	}
	// A relay event frame is ["EVENT", subscription, event]; a stored event
	// may arrive as ["EVENT", event].
	payload := frame[1]
	if len(frame) >= 3 {
		payload = frame[2]
	}
	var ev event
	if err := json.Unmarshal(payload, &ev); err != nil {
		return event{}, false
	}
	return ev, true
}

func (a *Adapter) ingest(ctx context.Context, ev *event) error {
	if ev == nil || ev.PubKey == "" || ev.ID == "" || ev.PubKey == a.pub {
		return nil
	}
	if !validEventID(ev) {
		a.log.WarnContext(ctx, "buzz: event id mismatch", "event", ev.ID)
		return nil
	}
	if !a.dedupe.Claim("buzz:"+ev.ID, a.now()) {
		return nil
	}
	group := ev.Kind == kindChannel
	chat := ev.PubKey
	if group {
		if channels := tagValues(ev.Tags, "h"); len(channels) > 0 {
			chat = channels[0]
		}
	}
	in := &gateway.Inbound{
		Platform:  "buzz",
		Account:   a.pub,
		ChatID:    chat,
		SenderID:  ev.PubKey,
		UpdateID:  "buzz:" + ev.ID,
		Text:      ev.Content,
		Group:     group,
		Mentioned: containsTag(ev.Tags, "p", a.pub),
	}
	if err := gwplat.Dispatch(ctx, a.handler, in); err != nil {
		return fmt.Errorf("buzz: dispatch: %w", err)
	}
	return nil
}

func validEventID(ev *event) bool {
	id, err := eventID(ev)
	return err == nil && id == ev.ID
}

func containsTag(tags [][]string, key, value string) bool {
	return slices.Contains(tagValues(tags, key), value)
}

// Publish signs and sends one event on the active relay connection. The reply
// text is already rendered by the outbox.
func (a *Adapter) Publish(ctx context.Context, chat string, group bool, text string) error {
	kind := kindNote
	tags := [][]string{{"p", chat}}
	if group {
		kind = kindChannel
		tags = [][]string{{"h", chat}}
	}
	aux := make([]byte, 32)
	if _, err := io.ReadFull(a.rand, aux); err != nil {
		return fmt.Errorf("buzz: auxiliary randomness: %w", err)
	}
	ev, err := signEvent(a.priv, kind, text, tags, a.now(), aux)
	if err != nil {
		return err
	}
	frame, err := json.Marshal([]any{"EVENT", ev})
	if err != nil {
		return fmt.Errorf("buzz: publish frame: %w", err)
	}
	return a.write(ctx, frame)
}

func (a *Adapter) write(ctx context.Context, payload []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.conn == nil {
		return ErrNoConn
	}
	if err := a.conn.Write(ctx, websocket.MessageText, payload); err != nil {
		return fmt.Errorf("buzz: relay write: %w", err)
	}
	return nil
}

func (a *Adapter) setConn(conn *websocket.Conn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.conn = conn
}

// Outbox exposes the shared outbound seam to the wiring.
func (a *Adapter) Outbox() *gwplat.Outbox { return a.out }

// closeResponse closes a handshake response; websocket.Dial sets Body to nil
// on success, so only a refused handshake carries one.
func closeResponse(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	if err := resp.Body.Close(); err != nil {
		return
	}
}
