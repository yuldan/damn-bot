// Package gwmatrix implements the Matrix client-server surface of PLAT-011:
// sync long-poll, room messages, mentions, DM/thread mapping, sends through the
// shared outbox, and the seam for pure-Go E2EE (internal/gwmatrix/e2ee).
// Edits are never sent: Matrix history is permanent (GAP-GW-10).
package gwmatrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

// ErrConfig rejects an unusable activation.
var ErrConfig = errors.New("gwmatrix: invalid config")

// ErrNeedsAttention is a permanent homeserver failure (invalid token).
var ErrNeedsAttention = errors.New("gwmatrix: needs owner attention")

// AttentionError unwraps to the needs_attention verdict and to
// module_unavailable so the module host stops retrying.
type AttentionError struct{ Reason string }

// Error implements error.
func (e *AttentionError) Error() string {
	return "module_unavailable{matrix_" + e.Reason + "} (needs_attention)"
}

// Unwrap exposes both verdicts.
func (e *AttentionError) Unwrap() []error {
	return []error{ErrNeedsAttention, gwplat.Unavailablef("matrix", "matrix_%s", e.Reason)}
}

// Encrypted is the seam of the pure-Go E2EE layer. A nil implementation keeps
// the adapter to unencrypted rooms; encrypted events are then skipped instead
// of being sent to the model.
type Encrypted interface {
	// DecryptRoom returns the plaintext event JSON of a room event.
	DecryptRoom(ctx context.Context, roomID string, content json.RawMessage) ([]byte, error)
	// EncryptRoom returns the m.room.encrypted content for plaintext event JSON.
	EncryptRoom(ctx context.Context, roomID string, plaintext []byte) (json.RawMessage, error)
	// HandleToDevice consumes one to-device event (room keys, olm messages).
	HandleToDevice(ctx context.Context, sender string, content json.RawMessage) (bool, error)
}

// Config is the explicit activation of the adapter; the zero value is dormant.
type Config struct {
	Enabled     bool
	Homeserver  string
	UserID      string
	DeviceID    string
	HTTPClient  *http.Client
	SyncTimeout time.Duration
	Backoff     gwplat.Backoff
	Crypto      Encrypted
	Log         *slog.Logger
	Now         func() time.Time
}

// Adapter long-polls one Matrix account.
type Adapter struct {
	cfg     Config
	out     *gwplat.Outbox
	handler gwplat.Handler
	token   string
	userID  string
	since   string
	txn     int64
	dedupe  *gwplat.Dedupe
	log     *slog.Logger
	now     func() time.Time
	client  *http.Client

	mu     sync.Mutex
	direct map[string]bool
}

// New validates the explicit config and loads the access token from the secret
// seam only; it makes no request until Run.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource, out *gwplat.Outbox, h gwplat.Handler) (*Adapter, error) {
	if desc == nil || desc.Name != "matrix" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("gwmatrix: activation: %w", err)
	}
	if err := validateHomeserver(cfg.Homeserver); err != nil {
		return nil, err
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("gwmatrix: secret: %w", err)
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
		// Egress is injected (E-05): the composition root supplies a netx client.
		return nil, fmt.Errorf("%w: HTTP client is required", ErrConfig)
	}
	client := cfg.HTTPClient
	local := *cfg
	if local.SyncTimeout <= 0 {
		local.SyncTimeout = 30 * time.Second
	}
	return &Adapter{
		cfg: local, out: out, handler: h, token: values["matrix_access_token"],
		userID: local.UserID, dedupe: gwplat.NewDedupe(8192, 30*time.Minute),
		log: logger, now: now, client: client, direct: map[string]bool{},
	}, nil
}

func validateHomeserver(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("%w: homeserver URL", ErrConfig)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("%w: homeserver must be https or loopback http", ErrConfig)
}

// Run holds the sync loop; a permanent token failure stops it.
func (a *Adapter) Run(ctx context.Context) error {
	backoff := a.cfg.Backoff
	for attempt := 1; ; attempt++ {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		err := a.Sync(ctx)
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if errors.Is(err, ErrNeedsAttention) {
			return err
		}
		a.log.WarnContext(ctx, "gwmatrix sync ended", "attempt", attempt, "err", err)
		if waitErr := backoff.Wait(ctx, attempt); waitErr != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			return fmt.Errorf("gwmatrix: reconnect: %w", waitErr)
		}
	}
}

// Sync performs one long-poll and admits every new event.
func (a *Adapter) Sync(ctx context.Context) error {
	if err := a.identify(ctx); err != nil {
		return err
	}
	query := url.Values{}
	query.Set("timeout", strconv.FormatInt(a.cfg.SyncTimeout.Milliseconds(), 10))
	if a.since != "" {
		query.Set("since", a.since)
	}
	var resp syncResponse
	if err := a.do(ctx, http.MethodGet, "/_matrix/client/v3/sync?"+query.Encode(), nil, &resp); err != nil {
		return err
	}
	if resp.NextBatch != "" {
		a.since = resp.NextBatch
	}
	for i := range resp.ToDevice.Events {
		a.handleToDevice(ctx, &resp.ToDevice.Events[i])
	}
	for roomID, joined := range resp.Rooms.Join {
		for i := range joined.Timeline.Events {
			a.handleEvent(ctx, roomID, &joined.Timeline.Events[i])
		}
	}
	return nil
}

// identify fills the account id from /whoami and loads the m.direct map once.
func (a *Adapter) identify(ctx context.Context) error {
	if a.userID != "" && len(a.direct) > 0 {
		return nil
	}
	var who struct {
		UserID   string `json:"user_id"`
		DeviceID string `json:"device_id"`
	}
	if err := a.do(ctx, http.MethodGet, "/_matrix/client/v3/account/whoami", nil, &who); err != nil {
		return err
	}
	if who.UserID == "" {
		return fmt.Errorf("%w: whoami returned no user_id", ErrConfig)
	}
	a.userID = who.UserID
	if a.cfg.DeviceID == "" {
		a.cfg.DeviceID = who.DeviceID
	}
	return a.loadDirect(ctx)
}

func (a *Adapter) loadDirect(ctx context.Context) error {
	var direct map[string][]string
	// Absent m.direct account data is not a failure: everything is a group.
	if err := a.do(ctx, http.MethodGet, "/_matrix/client/v3/user/"+url.PathEscape(a.userID)+"/account_data/m.direct", nil, &direct); err == nil {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, rooms := range direct {
			for _, room := range rooms {
				a.direct[room] = true
			}
		}
	}
	return nil
}

func (a *Adapter) isDirect(roomID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.direct[roomID]
}

// handleToDevice hands one encrypted to-device event to the E2EE seam.
func (a *Adapter) handleToDevice(ctx context.Context, ev *event) {
	if a.cfg.Crypto == nil || ev.Type != "m.room.encrypted" {
		return
	}
	if _, err := a.cfg.Crypto.HandleToDevice(ctx, ev.Sender, ev.Content); err != nil {
		a.log.WarnContext(ctx, "gwmatrix to-device", "err", err)
	}
}

// handleEvent maps one room timeline event to an inbound message.
func (a *Adapter) handleEvent(ctx context.Context, roomID string, ev *event) {
	if ev.EventID == "" || ev.Sender == "" || ev.Sender == a.userID {
		return
	}
	content, err := a.plaintext(ctx, roomID, ev)
	if err != nil || content == nil {
		return
	}
	msg := messageContent(content)
	if msg == nil || msg.MsgType != "m.text" || strings.TrimSpace(msg.Body) == "" {
		return
	}
	if !a.dedupe.Claim("matrix:"+ev.EventID, a.now()) {
		return
	}
	in := &gateway.Inbound{
		Platform: "matrix", Account: a.userID, ChatID: roomID,
		SenderID:  ev.Sender,
		UpdateID:  "matrix:" + ev.EventID,
		Text:      msg.Body,
		Group:     !a.isDirect(roomID),
		Mentioned: a.mentions(msg, ev.Sender),
		ThreadID:  msg.ThreadID(),
	}
	if err := gwplat.Dispatch(ctx, a.handler, in); err != nil {
		a.log.WarnContext(ctx, "gwmatrix dispatch", "err", err)
	}
}

// plaintext returns the event content, decrypting m.room.encrypted through the
// E2EE seam. A nil, nil answer means the event is skipped.
func (a *Adapter) plaintext(ctx context.Context, roomID string, ev *event) (json.RawMessage, error) {
	switch ev.Type {
	case "m.room.message":
		return ev.Content, nil
	case "m.room.encrypted":
		if a.cfg.Crypto == nil {
			a.log.DebugContext(ctx, "gwmatrix: encrypted event skipped (E2EE off)")
			return nil, nil
		}
		raw, err := a.cfg.Crypto.DecryptRoom(ctx, roomID, ev.Content)
		if err != nil {
			return nil, fmt.Errorf("gwmatrix: decrypt room: %w", err)
		}
		var inner event
		if err := json.Unmarshal(raw, &inner); err != nil {
			return nil, fmt.Errorf("gwmatrix: decrypted event: %w", err)
		}
		return inner.Content, nil
	}
	return nil, nil
}

func (a *Adapter) mentions(msg *message, sender string) bool {
	if a.userID == "" || sender == a.userID {
		return false
	}
	if slices.Contains(msg.Mentions.UserIDs, a.userID) {
		return true
	}
	return strings.Contains(msg.Body, a.userID)
}

// Send renders one reply and sends it as a plaintext m.room.message. There is
// no edit path: MatrixCaps has no edit stream (GAP-GW-10).
func (a *Adapter) Send(ctx context.Context, roomID, markdown string) error {
	if roomID == "" {
		return fmt.Errorf("%w: empty room", ErrConfig)
	}
	if err := a.out.Reply(ctx, gwplat.Reply{Chat: roomID, Markdown: markdown, Send: a.sendText(roomID)}); err != nil {
		return fmt.Errorf("gwmatrix: reply: %w", err)
	}
	return nil
}

func (a *Adapter) sendText(roomID string) gwplat.SendFunc {
	return func(ctx context.Context, text string) error {
		a.txn++
		txn := fmt.Sprintf("damnbot%d", a.txn)
		path := "/_matrix/client/v3/rooms/" + url.PathEscape(roomID) + "/send/m.room.message/" + txn
		body := map[string]string{"msgtype": "m.text", "body": text}
		var out map[string]string
		return a.do(ctx, http.MethodPut, path, body, &out)
	}
}

// do performs one authenticated client-server request.
func (a *Adapter) do(ctx context.Context, method, path string, body, out any) error {
	var reader *strings.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("gwmatrix: encode: %w", err)
		}
		reader = strings.NewReader(string(raw))
	} else {
		reader = strings.NewReader("")
	}
	endpoint := strings.TrimRight(a.cfg.Homeserver, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("gwmatrix: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("gwmatrix: transport: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			return
		}
	}()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &AttentionError{Reason: "auth_failed"}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("gwmatrix: %s %s: status %d", method, path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("gwmatrix: decode: %w", err)
	}
	return nil
}

type syncResponse struct {
	NextBatch string `json:"next_batch"`
	ToDevice  struct {
		Events []event `json:"events"`
	} `json:"to_device"`
	Rooms struct {
		Join map[string]struct {
			Timeline struct {
				Events []event `json:"events"`
			} `json:"timeline"`
		} `json:"join"`
	} `json:"rooms"`
}

type event struct {
	Type    string          `json:"type"`
	Sender  string          `json:"sender"`
	EventID string          `json:"event_id"`
	Content json.RawMessage `json:"content"`
}

type message struct {
	MsgType  string          `json:"msgtype"`
	Body     string          `json:"body"`
	Mentions mentionsField   `json:"m.mentions"`
	Relates  json.RawMessage `json:"m.relates_to"`
}

type mentionsField struct {
	UserIDs []string `json:"user_ids"`
}

// ThreadID returns the Matrix thread root of a threaded event.
func (m *message) ThreadID() string {
	if len(m.Relates) == 0 {
		return ""
	}
	var rel struct {
		RelType string `json:"rel_type"`
		EventID string `json:"event_id"`
	}
	if json.Unmarshal(m.Relates, &rel) != nil || rel.RelType != "m.thread" {
		return ""
	}
	return rel.EventID
}

func messageContent(raw json.RawMessage) *message {
	var msg message
	if json.Unmarshal(raw, &msg) != nil {
		return nil
	}
	return &msg
}
