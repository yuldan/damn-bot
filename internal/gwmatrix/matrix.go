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
	"github.com/damnbot/damnbot/internal/gwmatrix/e2ee"
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

// Encrypted is the seam of the pure-Go E2EE layer (internal/gwmatrix/e2ee).
// A nil implementation keeps the adapter to unencrypted rooms; encrypted events
// are then skipped instead of being sent to the model.
type Encrypted interface {
	DecryptRoom(ctx context.Context, roomID string, content json.RawMessage) ([]byte, error)
	EncryptRoom(ctx context.Context, roomID string, plaintext []byte) (json.RawMessage, error)
	HandleToDevice(ctx context.Context, sender string, content json.RawMessage) (bool, error)
}

// Keyring is the device/key-sharing half of the E2EE layer.
type Keyring interface {
	DeviceKeys(ctx context.Context) (json.RawMessage, map[string]string, error)
	MarkPublished()
	NewOutboundRoom(ctx context.Context, roomID string) (*e2ee.RoomKey, error)
	AddRoomKey(ctx context.Context, roomID string, key *e2ee.RoomKey) error
	EncryptToDevice(ctx context.Context, device *e2ee.Device, plaintext []byte) (json.RawMessage, error)
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
	Keys        Keyring
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

	mu        sync.Mutex
	direct    map[string]bool
	encrypted map[string]bool
	roomKeys  map[string]*e2ee.RoomKey
	keysDone  bool
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
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	local := *cfg
	if local.SyncTimeout <= 0 {
		local.SyncTimeout = 30 * time.Second
	}
	return &Adapter{
		cfg: local, out: out, handler: h, token: values["matrix_access_token"],
		userID: local.UserID, dedupe: gwplat.NewDedupe(8192, 30*time.Minute),
		log: logger, now: now, client: client, direct: map[string]bool{},
		encrypted: map[string]bool{}, roomKeys: map[string]*e2ee.RoomKey{},
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

// Outbox exposes the shared outbound seam to the wiring.
func (a *Adapter) Outbox() *gwplat.Outbox { return a.out }

// Run holds the sync loop; a permanent token failure stops it.
func (a *Adapter) Run(ctx context.Context) error {
	backoff := a.cfg.Backoff
	for attempt := 1; ; attempt++ {
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
		for i := range joined.State.Events {
			if joined.State.Events[i].Type == "m.room.encryption" {
				a.mu.Lock()
				a.encrypted[roomID] = true
				a.mu.Unlock()
			}
		}
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
	if err := a.loadDirect(ctx); err != nil {
		return err
	}
	return a.uploadKeys(ctx)
}

// uploadKeys publishes the device and one-time keys once per process.
func (a *Adapter) uploadKeys(ctx context.Context) error {
	if a.cfg.Keys == nil || a.keysDone {
		return nil
	}
	deviceKeys, oneTime, err := a.cfg.Keys.DeviceKeys(ctx)
	if err != nil {
		return fmt.Errorf("gwmatrix: device keys: %w", err)
	}
	body := map[string]any{"device_keys": deviceKeys, "one_time_keys": oneTime}
	var out map[string]any
	if err := a.do(ctx, http.MethodPost, "/_matrix/client/v3/keys/upload", body, &out); err != nil {
		return err
	}
	a.cfg.Keys.MarkPublished()
	a.keysDone = true
	return nil
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
	if err != nil {
		a.log.WarnContext(ctx, "gwmatrix: event decrypt failed", "room", roomID, "err", err)
		return
	}
	if content == nil {
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
		message := map[string]string{"msgtype": "m.text", "body": text}
		eventType := "m.room.message"
		if a.isEncrypted(roomID) {
			sealed, err := a.seal(ctx, roomID, message)
			if err != nil {
				return err
			}
			return a.sendEvent(ctx, roomID, "m.room.encrypted", sealed)
		}
		return a.sendEvent(ctx, roomID, eventType, message)
	}
}

func (a *Adapter) isEncrypted(roomID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.encrypted[roomID]
}

// seal shares the room key with the member devices once, then encrypts the
// event with the Megolm session.
func (a *Adapter) seal(ctx context.Context, roomID string, message map[string]string) (json.RawMessage, error) {
	if a.cfg.Crypto == nil || a.cfg.Keys == nil {
		return nil, fmt.Errorf("gwmatrix: encrypted room without E2EE")
	}
	a.mu.Lock()
	key := a.roomKeys[roomID]
	a.mu.Unlock()
	if key == nil {
		created, err := a.cfg.Keys.NewOutboundRoom(ctx, roomID)
		if err != nil {
			return nil, fmt.Errorf("gwmatrix: room key: %w", err)
		}
		if err := a.shareRoomKey(ctx, roomID, created); err != nil {
			return nil, err
		}
		key = created
		a.mu.Lock()
		a.roomKeys[roomID] = key
		a.mu.Unlock()
	}
	plaintext, err := json.Marshal(map[string]any{"type": "m.room.message", "room_id": roomID, "content": message})
	if err != nil {
		return nil, fmt.Errorf("gwmatrix: encode event: %w", err)
	}
	sealed, err := a.cfg.Crypto.EncryptRoom(ctx, roomID, plaintext)
	if err != nil {
		return nil, fmt.Errorf("gwmatrix: encrypt event: %w", err)
	}
	return sealed, nil
}

func (a *Adapter) sendEvent(ctx context.Context, roomID, eventType string, content any) error {
	a.txn++
	txn := fmt.Sprintf("damnbot%d", a.txn)
	path := "/_matrix/client/v3/rooms/" + url.PathEscape(roomID) + "/send/" + url.PathEscape(eventType) + "/" + txn
	var out map[string]string
	if err := a.do(ctx, http.MethodPut, path, content, &out); err != nil {
		return err
	}
	return nil
}

// shareRoomKey sends the Megolm key to every joined member device through
// to-device Olm messages.
func (a *Adapter) shareRoomKey(ctx context.Context, roomID string, key *e2ee.RoomKey) error {
	var members struct {
		Joined map[string]json.RawMessage `json:"joined"`
	}
	if err := a.do(ctx, http.MethodGet, "/_matrix/client/v3/rooms/"+url.PathEscape(roomID)+"/joined_members", nil, &members); err != nil {
		return err
	}
	var query struct {
		DeviceKeys map[string]map[string]struct {
			Keys map[string]string `json:"keys"`
		} `json:"device_keys"`
	}
	requested := map[string][]string{}
	for user := range members.Joined {
		if user != a.userID {
			requested[user] = []string{}
		}
	}
	if err := a.do(ctx, http.MethodPost, "/_matrix/client/v3/keys/query", map[string]any{"device_keys": requested}, &query); err != nil {
		return err
	}
	messages := map[string]map[string]json.RawMessage{}
	for user, devices := range query.DeviceKeys {
		for deviceID, device := range devices {
			curve := device.Keys["curve25519:"+deviceID]
			if curve == "" {
				continue
			}
			otk, err := a.claimKey(ctx, user, deviceID)
			if err != nil {
				return err
			}
			plaintext, err := json.Marshal(map[string]any{
				"type":   "m.room_key",
				"sender": a.userID,
				"content": map[string]string{"algorithm": e2ee.AlgorithmMegolm, "room_id": roomID,
					"session_id": key.SessionID, "session_key": key.SessionKey},
			})
			if err != nil {
				return fmt.Errorf("gwmatrix: encode room key: %w", err)
			}
			sealed, err := a.cfg.Keys.EncryptToDevice(ctx, &e2ee.Device{
				UserID: user, DeviceID: deviceID, Curve25519: curve,
				Ed25519: device.Keys["ed25519:"+deviceID], OneTimeKey: otk,
			}, plaintext)
			if err != nil {
				return fmt.Errorf("gwmatrix: seal room key: %w", err)
			}
			if messages[user] == nil {
				messages[user] = map[string]json.RawMessage{}
			}
			messages[user][deviceID] = sealed
		}
	}
	if len(messages) == 0 {
		return nil
	}
	a.txn++
	path := "/_matrix/client/v3/sendToDevice/m.room.encrypted/damnbot" + fmt.Sprint(a.txn)
	var out map[string]any
	return a.do(ctx, http.MethodPut, path, map[string]any{"messages": messages}, &out)
}

func (a *Adapter) claimKey(ctx context.Context, userID, deviceID string) (string, error) {
	body := map[string]any{"one_time_keys": map[string]map[string]string{userID: {deviceID: "signed_curve25519"}}}
	var resp struct {
		OneTimeKeys map[string]map[string]map[string]struct {
			Key string `json:"key"`
		} `json:"one_time_keys"`
	}
	if err := a.do(ctx, http.MethodPost, "/_matrix/client/v3/keys/claim", body, &resp); err != nil {
		return "", err
	}
	for _, entry := range resp.OneTimeKeys[userID][deviceID] {
		if entry.Key != "" {
			return entry.Key, nil
		}
	}
	return "", nil
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
			State struct {
				Events []event `json:"events"`
			} `json:"state"`
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
