// Package e2ee is the pure-Go Matrix E2EE layer of PLAT-011 (M-04): Olm device
// sessions and Megolm room sessions from mautrix's goolm, with no CGO. Room
// keys travel as to-device m.room_key events; the sealed payload is never
// written outside the injected Store.
package e2ee

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"maunium.net/go/mautrix/crypto/goolm/account"
	"maunium.net/go/mautrix/crypto/goolm/session"
	"maunium.net/go/mautrix/crypto/olm"
	"maunium.net/go/mautrix/id"
)

// Crypto failures. Compared with errors.Is.
var (
	ErrConfig      = errors.New("e2ee: invalid config")
	ErrNoSession   = errors.New("e2ee: no session for the device")
	ErrNoRoomKey   = errors.New("e2ee: no inbound group session")
	ErrBadPayload  = errors.New("e2ee: malformed payload")
	ErrUnknownType = errors.New("e2ee: unknown to-device type")
)

// Algorithms of the Matrix E2EE spec.
const (
	AlgorithmOlm    = "m.olm.v1.curve25519-aes-sha2"
	AlgorithmMegolm = "m.megolm.v1.aes-sha2"
)

// Store persists the pickled account so a restart keeps the same device keys.
type Store interface {
	Load(ctx context.Context) ([]byte, bool, error)
	Save(ctx context.Context, pickled []byte) error
}

// Device is the public key set of one Matrix device.
type Device struct {
	UserID     string
	DeviceID   string
	Curve25519 string
	Ed25519    string
	OneTimeKey string
}

// RoomKey is the Megolm session key shared with the room devices.
type RoomKey struct {
	RoomID     string
	SessionID  string
	SessionKey string
}

// Config is the explicit activation of the crypto layer.
type Config struct {
	UserID      string
	DeviceID    string
	Store       Store
	PickleKey   []byte
	OneTimeKeys uint
}

// Crypto owns one Olm account and its Olm/Megolm sessions.
type Crypto struct {
	userID, deviceID string
	store            Store
	pickleKey        []byte

	mu        sync.Mutex
	account   *account.Account
	olm       map[string]olm.Session
	outbound  map[string]*session.MegolmOutboundSession
	inbound   map[string]*session.MegolmInboundSession
	published bool
}

// New loads the pickled account or creates a new one, then generates one-time
// keys. It never dials the network.
func New(ctx context.Context, cfg *Config) (*Crypto, error) {
	if cfg == nil || cfg.UserID == "" || cfg.DeviceID == "" || len(cfg.PickleKey) < 32 {
		return nil, fmt.Errorf("%w: user/device/pickle key", ErrConfig)
	}
	c := &Crypto{
		userID: cfg.UserID, deviceID: cfg.DeviceID, store: cfg.Store, pickleKey: cfg.PickleKey,
		olm: map[string]olm.Session{}, outbound: map[string]*session.MegolmOutboundSession{},
		inbound: map[string]*session.MegolmInboundSession{},
	}
	if err := c.load(ctx); err != nil {
		return nil, err
	}
	keys := cfg.OneTimeKeys
	if keys == 0 {
		keys = 20
	}
	if err := c.account.GenOneTimeKeys(keys); err != nil {
		return nil, fmt.Errorf("e2ee: one-time keys: %w", err)
	}
	return c, nil
}

func (c *Crypto) load(ctx context.Context) error {
	acc, found, err := c.loadStored(ctx)
	if err != nil {
		return err
	}
	if found {
		c.account = acc
		return nil
	}
	acc, err = account.NewAccount()
	if err != nil {
		return fmt.Errorf("e2ee: new account: %w", err)
	}
	c.account = acc
	return c.persist(ctx)
}

func (c *Crypto) loadStored(ctx context.Context) (*account.Account, bool, error) {
	if c.store == nil {
		return nil, false, nil
	}
	pickled, found, err := c.store.Load(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("e2ee: load: %w", err)
	}
	if !found {
		return nil, false, nil
	}
	acc, err := account.AccountFromPickled(pickled, c.pickleKey)
	if err != nil {
		return nil, false, fmt.Errorf("e2ee: unpickle account: %w", err)
	}
	return acc, true, nil
}

func (c *Crypto) persist(ctx context.Context) error {
	if c.store == nil {
		return nil
	}
	pickled, err := c.account.Pickle(c.pickleKey)
	if err != nil {
		return fmt.Errorf("e2ee: pickle account: %w", err)
	}
	if err := c.store.Save(ctx, pickled); err != nil {
		return fmt.Errorf("e2ee: save account: %w", err)
	}
	return nil
}

// Identity returns the account's Ed25519 and Curve25519 keys.
func (c *Crypto) Identity() (id.Ed25519, id.Curve25519, error) {
	ed, curve, err := c.account.IdentityKeys()
	if err != nil {
		return "", "", fmt.Errorf("e2ee: identity keys: %w", err)
	}
	return ed, curve, nil
}

// DeviceKeys returns the signed device_keys object and its one-time keys.
func (c *Crypto) DeviceKeys(_ context.Context) (json.RawMessage, map[string]string, error) {
	ed, curve, err := c.account.IdentityKeys()
	if err != nil {
		return nil, nil, fmt.Errorf("e2ee: identity keys: %w", err)
	}
	otks, err := c.account.OneTimeKeys()
	if err != nil {
		return nil, nil, fmt.Errorf("e2ee: one-time keys: %w", err)
	}
	keys := map[string]string{"curve25519:" + c.deviceID: string(curve), "ed25519:" + c.deviceID: string(ed)}
	payload := map[string]any{
		"user_id":    c.userID,
		"device_id":  c.deviceID,
		"algorithms": []string{"m.olm.v1.curve25519-aes-sha2", "m.megolm.v1.aes-sha2"},
		"keys":       keys,
	}
	signable, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("e2ee: encode device keys: %w", err)
	}
	signature, err := c.account.Sign(signable)
	if err != nil {
		return nil, nil, fmt.Errorf("e2ee: sign device keys: %w", err)
	}
	payload["signatures"] = map[string]map[string]string{c.userID: {"ed25519:" + c.deviceID: string(signature)}}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("e2ee: encode: %w", err)
	}
	out := make(map[string]string, len(otks))
	for key, value := range otks {
		out[key] = string(value)
	}
	return raw, out, nil
}

// MarkPublished records the uploaded keys so they are not offered twice.
func (c *Crypto) MarkPublished() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.account.MarkKeysAsPublished()
	c.published = true
}

// EncryptToDevice seals plaintext for one device with Olm. A missing one-time
// key means the caller must claim one first (ErrNoSession).
func (c *Crypto) EncryptToDevice(_ context.Context, device *Device, plaintext []byte) (json.RawMessage, error) {
	if device == nil || device.Curve25519 == "" || device.OneTimeKey == "" {
		return nil, fmt.Errorf("%w: device keys", ErrConfig)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	sess, ok := c.olm[device.Curve25519]
	if !ok {
		created, err := c.account.NewOutboundSession(id.Curve25519(device.Curve25519), id.Curve25519(device.OneTimeKey))
		if err != nil {
			return nil, fmt.Errorf("e2ee: outbound olm: %w", err)
		}
		sess = created
		c.olm[device.Curve25519] = sess
	}
	msgType, body, err := sess.Encrypt(plaintext)
	if err != nil {
		return nil, fmt.Errorf("e2ee: olm encrypt: %w", err)
	}
	_, curve, err := c.account.IdentityKeys()
	if err != nil {
		return nil, fmt.Errorf("e2ee: identity keys: %w", err)
	}
	content := map[string]any{
		"algorithm":  AlgorithmOlm,
		"sender_key": string(curve),
		"ciphertext": map[string]any{
			device.Curve25519: map[string]any{"type": int(msgType), "body": base64.RawStdEncoding.EncodeToString(body)},
		},
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("e2ee: encode olm content: %w", err)
	}
	return raw, nil
}

// DecryptToDevice opens one m.room.encrypted to-device payload.
func (c *Crypto) DecryptToDevice(_ context.Context, sender string, content json.RawMessage) ([]byte, error) {
	var payload struct {
		Algorithm  string `json:"algorithm"`
		SenderKey  string `json:"sender_key"`
		Ciphertext map[string]struct {
			Type int    `json:"type"`
			Body string `json:"body"`
		} `json:"ciphertext"`
	}
	if err := json.Unmarshal(content, &payload); err != nil || payload.Algorithm != AlgorithmOlm {
		return nil, fmt.Errorf("%w: olm content", ErrBadPayload)
	}
	_, curve, err := c.account.IdentityKeys()
	if err != nil {
		return nil, fmt.Errorf("e2ee: identity keys: %w", err)
	}
	cipher, ok := payload.Ciphertext[string(curve)]
	if !ok {
		return nil, fmt.Errorf("%w: not for this device", ErrBadPayload)
	}
	body, err := base64.RawStdEncoding.DecodeString(cipher.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: ciphertext base64", ErrBadPayload)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	msgType := id.OlmMsgType(cipher.Type)
	sess, ok := c.olm[payload.SenderKey]
	if !ok && msgType == id.OlmMsgTypePreKey {
		senderKey := id.Curve25519(payload.SenderKey)
		created, ierr := c.account.NewInboundSessionFrom(&senderKey, string(body))
		if ierr != nil {
			return nil, fmt.Errorf("e2ee: inbound olm: %w", ierr)
		}
		sess = created
		c.olm[payload.SenderKey] = sess
	}
	if sess == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoSession, sender)
	}
	plain, err := sess.Decrypt(string(body), msgType)
	if err != nil {
		return nil, fmt.Errorf("e2ee: olm decrypt: %w", err)
	}
	return plain, nil
}

// NewOutboundRoom creates one Megolm session for the room and returns the key
// to share through to-device events.
func (c *Crypto) NewOutboundRoom(_ context.Context, roomID string) (*RoomKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	group, err := session.NewMegolmOutboundSession()
	if err != nil {
		return nil, fmt.Errorf("e2ee: new megolm session: %w", err)
	}
	sharing, err := group.SessionSharingMessage()
	if err != nil {
		return nil, fmt.Errorf("e2ee: megolm sharing message: %w", err)
	}
	c.outbound[roomID] = group
	return &RoomKey{RoomID: roomID, SessionID: string(group.ID()), SessionKey: base64.RawStdEncoding.EncodeToString(sharing)}, nil
}

// AddRoomKey installs an inbound Megolm session from a shared key.
func (c *Crypto) AddRoomKey(_ context.Context, roomID string, key *RoomKey) error {
	if key == nil || key.SessionKey == "" {
		return fmt.Errorf("%w: room key", ErrBadPayload)
	}
	sharing, err := base64.RawStdEncoding.DecodeString(key.SessionKey)
	if err != nil {
		return fmt.Errorf("%w: session key base64", ErrBadPayload)
	}
	group, err := session.NewMegolmInboundSession(sharing)
	if err != nil {
		return fmt.Errorf("e2ee: inbound megolm: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inbound[string(group.ID())] = group
	return nil
}

// EncryptRoom seals one event JSON for the room with its Megolm session.
func (c *Crypto) EncryptRoom(_ context.Context, roomID string, plaintext []byte) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	group, ok := c.outbound[roomID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoRoomKey, roomID)
	}
	sealed, err := group.Encrypt(plaintext)
	if err != nil {
		return nil, fmt.Errorf("e2ee: megolm encrypt: %w", err)
	}
	_, curve, err := c.account.IdentityKeys()
	if err != nil {
		return nil, fmt.Errorf("e2ee: identity keys: %w", err)
	}
	content := map[string]string{
		"algorithm":  AlgorithmMegolm,
		"sender_key": string(curve),
		"device_id":  c.deviceID,
		"session_id": string(group.ID()),
		"ciphertext": base64.RawStdEncoding.EncodeToString(sealed),
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("e2ee: encode megolm content: %w", err)
	}
	return raw, nil
}

// DecryptRoom opens one m.room.encrypted room event.
func (c *Crypto) DecryptRoom(_ context.Context, roomID string, content json.RawMessage) ([]byte, error) {
	var payload struct {
		Algorithm  string `json:"algorithm"`
		SessionID  string `json:"session_id"`
		Ciphertext string `json:"ciphertext"`
	}
	if err := json.Unmarshal(content, &payload); err != nil || payload.Algorithm != AlgorithmMegolm {
		return nil, fmt.Errorf("%w: megolm content", ErrBadPayload)
	}
	sealed, err := base64.RawStdEncoding.DecodeString(payload.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("%w: ciphertext base64", ErrBadPayload)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	group, ok := c.inbound[payload.SessionID]
	if !ok {
		return nil, fmt.Errorf("%w: %s in %s", ErrNoRoomKey, payload.SessionID, roomID)
	}
	plain, _, err := group.Decrypt(sealed)
	if err != nil {
		return nil, fmt.Errorf("e2ee: megolm decrypt: %w", err)
	}
	return plain, nil
}

// HandleToDevice consumes an Olm to-device event: m.room_key installs a room
// session; anything else is rejected as unknown.
func (c *Crypto) HandleToDevice(ctx context.Context, sender string, content json.RawMessage) (bool, error) {
	plain, err := c.DecryptToDevice(ctx, sender, content)
	if err != nil {
		return false, err
	}
	var envelope struct {
		Type    string `json:"type"`
		Content struct {
			Algorithm  string `json:"algorithm"`
			RoomID     string `json:"room_id"`
			SessionID  string `json:"session_id"`
			SessionKey string `json:"session_key"`
		} `json:"content"`
	}
	if err := json.Unmarshal(plain, &envelope); err != nil {
		return false, fmt.Errorf("%w: to-device envelope", ErrBadPayload)
	}
	if envelope.Type != "m.room_key" || envelope.Content.Algorithm != AlgorithmMegolm {
		return false, fmt.Errorf("%w: %s", ErrUnknownType, envelope.Type)
	}
	return true, c.AddRoomKey(ctx, envelope.Content.RoomID, &RoomKey{
		RoomID: envelope.Content.RoomID, SessionID: envelope.Content.SessionID, SessionKey: envelope.Content.SessionKey,
	})
}
