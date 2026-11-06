package gwmatrix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gwmatrix/e2ee"
)

type pickleStore struct{ pickled []byte }

func (p *pickleStore) Load(context.Context) ([]byte, bool, error) {
	return p.pickled, p.pickled != nil, nil
}

func (p *pickleStore) Save(_ context.Context, pickled []byte) error {
	p.pickled = append([]byte(nil), pickled...)
	return nil
}

func newCrypto(t *testing.T, user, device string) *e2ee.Crypto {
	t.Helper()
	c, err := e2ee.New(t.Context(), &e2ee.Config{
		UserID: user, DeviceID: device, Store: &pickleStore{},
		PickleKey: bytes.Repeat([]byte{0x17}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func peerKeys(t *testing.T, peer *e2ee.Crypto) (device, curve, ed, otk string) {
	t.Helper()
	raw, otks, err := peer.DeviceKeys(t.Context())
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
		return "PEER", payload.Keys["curve25519:PEER"], payload.Keys["ed25519:PEER"], key
	}
	t.Fatal("peer has no one-time key")
	return "", "", "", ""
}

// encryptedSync builds one sync response carrying the room's encryption state
// and a Megolm-sealed timeline event.
func encryptedSync(t *testing.T, peerCrypto *e2ee.Crypto, peer, room string) string {
	t.Helper()
	event, err := json.Marshal(map[string]any{"type": "m.room.message", "content": map[string]string{"msgtype": "m.text", "body": "secret hello @bot:example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	inbound, err := peerCrypto.EncryptRoom(t.Context(), room, event)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"next_batch": "s2",
		"rooms": map[string]any{"join": map[string]any{room: map[string]any{
			"state":    map[string]any{"events": []map[string]any{{"type": "m.room.encryption", "content": map[string]string{"algorithm": e2ee.AlgorithmMegolm}}}},
			"timeline": map[string]any{"events": []map[string]any{{"type": "m.room.encrypted", "sender": peer, "event_id": "$enc", "content": inbound}}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// TestF7PLAT011Matrix is the PLAT-011 acceptance: a pure-Go Megolm event
// decrypts into a turn, and an outbound encrypted room event is sealed with a
// Megolm key the peer can decrypt after the to-device share.
func TestF7PLAT011Matrix(t *testing.T) {
	const (
		bot  = "@bot:example.test"
		peer = "@peer:example.test"
		room = "!enc:example.test"
	)
	botCrypto := newCrypto(t, bot, "BOT")
	peerCrypto := newCrypto(t, peer, "PEER")
	deviceID, curve, ed, otk := peerKeys(t, peerCrypto)
	key, kerr := peerCrypto.NewOutboundRoom(t.Context(), room)
	if kerr != nil {
		t.Fatal(kerr)
	}
	if aerr := botCrypto.AddRoomKey(t.Context(), room, key); aerr != nil {
		t.Fatal(aerr)
	}
	srv := newFakeHomeserver(t, encryptedSync(t, peerCrypto, peer, room))
	srv.configure(
		fmt.Sprintf(`{"device_keys":{%q:{%q:{"keys":{"curve25519:%s":%q,"ed25519:%s":%q}}}}}`, peer, deviceID, deviceID, curve, deviceID, ed),
		fmt.Sprintf(`{"one_time_keys":{%q:{%q:{"signed_curve25519:AAAAAQ":{"key":%q}}}}}`, peer, deviceID, otk),
		fmt.Sprintf(`{"joined":{%q:{},%q:{}}}`, bot, peer),
	)
	rec := newRecorder()
	ad, err := buildAdapter(t, &Config{Enabled: true, Homeserver: srv.srv.URL, Crypto: botCrypto, Keys: botCrypto, SyncTimeout: time.Second}, secrets{"matrix_access_token": "tok"}, rec, newScheduler(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := ad.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case in := <-rec.seen:
		if in.Text != "secret hello @bot:example.test" || in.ChatID != room {
			t.Fatalf("decrypted inbound = %+v", in)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no decrypted turn")
	}
	assertEncryptedSend(t, ad, srv, botCrypto, peerCrypto, room, peer, deviceID)
}

func assertEncryptedSend(t *testing.T, ad *Adapter, srv *fakeHomeserver, botCrypto, peerCrypto *e2ee.Crypto, room, peer, deviceID string) {
	t.Helper()
	if err := ad.Send(t.Context(), room, "hello **peer**"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(sentOf(srv)) == 0 && time.Now().Before(deadline) {
		select {
		case <-t.Context().Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
	_, _, sent, _ := srv.snapshot()
	srv.mu.Lock()
	toDevice := append([]map[string]any(nil), srv.toDevice...)
	srv.mu.Unlock()
	if len(sent) == 0 || sent[0]["algorithm"] != e2ee.AlgorithmMegolm || sent[0]["session_id"] == "" {
		t.Fatalf("outbound not encrypted: %v", sent)
	}
	if strings.Contains(fmt.Sprint(sent[0]), "hello") {
		t.Fatalf("outbound leaked plaintext: %v", sent[0])
	}
	if len(toDevice) != 1 {
		t.Fatalf("room key shares = %d, want 1", len(toDevice))
	}
	assertPeerDecrypts(t, botCrypto, peerCrypto, peer, deviceID, room, sent[0], toDevice[0])
}

func assertPeerDecrypts(t *testing.T, botCrypto, peerCrypto *e2ee.Crypto, peer, deviceID, room string, sent map[string]string, toDevice map[string]any) {
	t.Helper()
	messages, ok := toDevice["messages"].(map[string]any)
	if !ok {
		t.Fatalf("to-device payload = %v", toDevice)
	}
	user, ok := messages[peer].(map[string]any)
	if !ok {
		t.Fatalf("to-device has no peer: %v", messages)
	}
	contentRaw, err := json.Marshal(user[deviceID])
	if err != nil {
		t.Fatal(err)
	}
	if _, herr := peerCrypto.HandleToDevice(t.Context(), "@bot:example.test", contentRaw); herr != nil {
		t.Fatalf("peer room key: %v", herr)
	}
	sealed, err := json.Marshal(sent)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := peerCrypto.DecryptRoom(t.Context(), room, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plain), "peer") {
		t.Fatalf("peer plaintext = %q", plain)
	}
	_ = botCrypto
}
