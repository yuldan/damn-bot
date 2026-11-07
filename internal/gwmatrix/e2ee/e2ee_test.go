package e2ee

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type memStore struct {
	pickled []byte
	found   bool
}

func (m *memStore) Load(context.Context) ([]byte, bool, error) { return m.pickled, m.found, nil }

func (m *memStore) Save(_ context.Context, pickled []byte) error {
	m.pickled, m.found = append([]byte(nil), pickled...), true
	return nil
}

func newCrypto(t *testing.T, user, device string, store Store) *Crypto {
	t.Helper()
	key := bytes.Repeat([]byte{0x42}, 32)
	c, err := New(t.Context(), &Config{UserID: user, DeviceID: device, Store: store, PickleKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func deviceOf(t *testing.T, c *Crypto, user, device string) (*Device, string) {
	t.Helper()
	raw, otks, err := c.DeviceKeys(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Keys map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range otks {
		return &Device{
			UserID: user, DeviceID: device,
			Curve25519: payload.Keys["curve25519:"+device], Ed25519: payload.Keys["ed25519:"+device],
			OneTimeKey: key,
		}, key
	}
	t.Fatal("no one-time key")
	return nil, ""
}

// TestE2EEOlmMegolmRoundTrip proves the pure-Go path: one Olm to-device
// session carries the Megolm room key and both directions decrypt.
func TestE2EEOlmMegolmRoundTrip(t *testing.T) {
	alice := newCrypto(t, "@alice:example.test", "ALICE", &memStore{})
	bob := newCrypto(t, "@bob:example.test", "BOB", &memStore{})
	device, _ := deviceOf(t, bob, "@bob:example.test", "BOB")

	envelope := []byte(`{"type":"m.room_key","content":{"algorithm":"m.megolm.v1.aes-sha2"}}`)
	sealed, err := alice.EncryptToDevice(t.Context(), device, envelope)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := bob.DecryptToDevice(t.Context(), "@alice:example.test", sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, envelope) {
		t.Fatalf("olm plaintext = %q", opened)
	}

	room := "!room:example.test"
	key, err := alice.NewOutboundRoom(t.Context(), room)
	if err != nil {
		t.Fatal(err)
	}
	if key.SessionID == "" || key.SessionKey == "" {
		t.Fatalf("room key = %+v", key)
	}
	if aerr := bob.AddRoomKey(t.Context(), room, key); aerr != nil {
		t.Fatal(aerr)
	}
	event := []byte(`{"type":"m.room.message","content":{"body":"hello"}}`)
	content, err := alice.EncryptRoom(t.Context(), room, event)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte("hello")) {
		t.Fatal("megolm ciphertext leaked the plaintext")
	}
	plain, err := bob.DecryptRoom(t.Context(), room, content)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain, event) {
		t.Fatalf("megolm plaintext = %q", plain)
	}
}

// TestE2EEPickleRestart proves the device identity survives a restart.
func TestE2EEPickleRestart(t *testing.T) {
	store := &memStore{}
	first := newCrypto(t, "@bot:example.test", "BOT", store)
	ed1, curve1, err := first.Identity()
	if err != nil {
		t.Fatal(err)
	}
	second := newCrypto(t, "@bot:example.test", "BOT", store)
	ed2, curve2, err := second.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if ed1 != ed2 || curve1 != curve2 {
		t.Fatalf("identity changed across restart: %s/%s vs %s/%s", ed1, curve1, ed2, curve2)
	}
}

// TestE2EECorruptPayload is the negative control: malformed payloads are typed
// errors, never panics.
func TestE2EECorruptPayload(t *testing.T) {
	c := newCrypto(t, "@bot:example.test", "BOT", &memStore{})
	if _, err := c.DecryptRoom(t.Context(), "!r:example.test", json.RawMessage(`{"algorithm":"m.megolm.v1.aes-sha2","session_id":"x","ciphertext":"!!"}`)); !errors.Is(err, ErrBadPayload) {
		t.Fatalf("corrupt cipher = %v, want ErrBadPayload", err)
	}
	if _, err := c.DecryptRoom(t.Context(), "!r:example.test", json.RawMessage(`{"algorithm":"m.megolm.v1.aes-sha2","session_id":"nope","ciphertext":"AAAA"}`)); !errors.Is(err, ErrNoRoomKey) {
		t.Fatalf("unknown session = %v, want ErrNoRoomKey", err)
	}
	if err := c.AddRoomKey(t.Context(), "!r:example.test", &RoomKey{SessionKey: "!!"}); !errors.Is(err, ErrBadPayload) {
		t.Fatalf("corrupt room key = %v, want ErrBadPayload", err)
	}
	if _, err := c.DecryptToDevice(t.Context(), "@x:example.test", json.RawMessage(`{`)); !errors.Is(err, ErrBadPayload) {
		t.Fatalf("corrupt to-device = %v, want ErrBadPayload", err)
	}
}

// TestE2EERoomKeyOverOlm covers HandleToDevice: an Olm-sealed m.room_key
// installs the Megolm session and the room event decrypts.
func TestE2EERoomKeyOverOlm(t *testing.T) {
	alice := newCrypto(t, "@alice:example.test", "ALICE", &memStore{})
	bob := newCrypto(t, "@bob:example.test", "BOB", &memStore{})
	device, _ := deviceOf(t, bob, "@bob:example.test", "BOB")
	room := "!room:example.test"
	key, err := alice.NewOutboundRoom(t.Context(), room)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(map[string]any{
		"type": "m.room_key",
		"content": map[string]string{
			"algorithm": AlgorithmMegolm, "room_id": room,
			"session_id": key.SessionID, "session_key": key.SessionKey,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := alice.EncryptToDevice(t.Context(), device, envelope)
	if err != nil {
		t.Fatal(err)
	}
	handled, err := bob.HandleToDevice(t.Context(), "@alice:example.test", sealed)
	if err != nil || !handled {
		t.Fatalf("handle = %v/%v", handled, err)
	}
	event := []byte(`{"type":"m.room.message","content":{"body":"hello"}}`)
	content, err := alice.EncryptRoom(t.Context(), room, event)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := bob.DecryptRoom(t.Context(), room, content)
	if err != nil || !bytes.Equal(plain, event) {
		t.Fatalf("decrypt = %q err %v", plain, err)
	}
}
