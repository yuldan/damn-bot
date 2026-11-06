package gwmatrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/outq"
	"github.com/damnbot/damnbot/internal/gwmatrix/e2ee"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

type allowPacer struct{}

func (allowPacer) TakeChat(string, string, bool, time.Time) (bool, time.Duration) { return true, 0 }
func (allowPacer) Penalize(string, time.Time)                                     {}

type secrets map[string]string

func (s secrets) Get(_ context.Context, name string) (string, error) {
	value, ok := s[name]
	if !ok {
		return "", errors.New("missing")
	}
	return value, nil
}

type recorder struct {
	mu   sync.Mutex
	in   []gateway.Inbound
	seen chan gateway.Inbound
}

func newRecorder() *recorder { return &recorder{seen: make(chan gateway.Inbound, 8)} }

func (r *recorder) Handle(_ context.Context, in *gateway.Inbound) error {
	r.mu.Lock()
	r.in = append(r.in, *in)
	r.mu.Unlock()
	r.seen <- *in
	return nil
}

func newScheduler(t *testing.T) *outq.Scheduler {
	t.Helper()
	sched, err := outq.New(&outq.Options{Pacer: allowPacer{}, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		if runErr := sched.Run(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Errorf("scheduler: %v", runErr)
		}
	}()
	return sched
}

// fakeHomeserver serves whoami, m.direct, sync and send, and records every
// authenticated request.
type fakeHomeserver struct {
	srv        *httptest.Server
	mu         sync.Mutex
	auth       []string
	paths      []string
	sent       []map[string]string
	syncBody   string
	status     int
	requests   int
	uploaded   map[string]any
	deviceKeys string
	claimBody  string
	members    string
	toDevice   []map[string]any
}

func newFakeHomeserver(t *testing.T, syncBody string) *fakeHomeserver {
	t.Helper()
	f := &fakeHomeserver{syncBody: syncBody}
	f.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests++
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.paths = append(f.paths, r.URL.Path)
		status := f.status
		f.mu.Unlock()
		if status != 0 {
			rw.WriteHeader(status)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/account/whoami"):
			writeJSON(t, rw, `{"user_id":"@bot:example.test","device_id":"DEV1"}`)
		case strings.Contains(r.URL.Path, "/account_data/m.direct"):
			writeJSON(t, rw, `{"@alice:example.test":["!dm:example.test"]}`)
		case strings.HasSuffix(r.URL.Path, "/sync"):
			writeJSON(t, rw, f.syncBody)
		case strings.Contains(r.URL.Path, "/keys/upload"):
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(rw, err.Error(), http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.uploaded = payload
			f.mu.Unlock()
			writeJSON(t, rw, `{"one_time_key_counts":{"signed_curve25519":1}}`)
		case strings.Contains(r.URL.Path, "/keys/query"):
			f.mu.Lock()
			keys := f.deviceKeys
			f.mu.Unlock()
			writeJSON(t, rw, keys)
		case strings.Contains(r.URL.Path, "/keys/claim"):
			f.mu.Lock()
			claim := f.claimBody
			f.mu.Unlock()
			writeJSON(t, rw, claim)
		case strings.Contains(r.URL.Path, "/joined_members"):
			f.mu.Lock()
			members := f.members
			f.mu.Unlock()
			writeJSON(t, rw, members)
		case strings.Contains(r.URL.Path, "/sendToDevice/"):
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(rw, err.Error(), http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.toDevice = append(f.toDevice, payload)
			f.mu.Unlock()
			writeJSON(t, rw, `{}`)
		case strings.Contains(r.URL.Path, "/send/"):
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(rw, err.Error(), http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.sent = append(f.sent, payload)
			f.mu.Unlock()
			writeJSON(t, rw, `{"event_id":"$sent1"}`)
		default:
			rw.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func writeJSON(t *testing.T, rw http.ResponseWriter, body string) {
	t.Helper()
	if _, err := rw.Write([]byte(body)); err != nil {
		t.Errorf("write: %v", err)
	}
}

func (f *fakeHomeserver) snapshot() (paths []string, auth []string, sent []map[string]string, requests int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...), append([]string(nil), f.auth...), append([]map[string]string(nil), f.sent...), f.requests
}

func (f *fakeHomeserver) configure(deviceKeys, claim, members string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deviceKeys, f.claimBody, f.members = deviceKeys, claim, members
}

func (f *fakeHomeserver) setStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

func buildAdapter(t *testing.T, cfg *Config, src gwplat.SecretSource, h gwplat.Handler, sched *outq.Scheduler) (*Adapter, error) {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	desc, err := reg.Lookup("matrix")
	if err != nil {
		return nil, fmt.Errorf("lookup: %w", err)
	}
	out, err := gwplat.NewOutbox(&desc, sched)
	if err != nil {
		return nil, fmt.Errorf("outbox: %w", err)
	}
	ad, err := New(t.Context(), cfg, &desc, src, out, h)
	if err != nil {
		return nil, fmt.Errorf("new: %w", err)
	}
	return ad, nil
}

func mustAdapter(t *testing.T, srv *fakeHomeserver, h gwplat.Handler, crypto Encrypted) *Adapter {
	t.Helper()
	cfg := &Config{Enabled: true, Homeserver: srv.srv.URL, Crypto: crypto, SyncTimeout: time.Second, HTTPClient: netx.LoopbackClient()}
	if keys, ok := crypto.(Keyring); ok {
		cfg.Keys = keys
	}
	ad, err := buildAdapter(t, cfg, secrets{"matrix_access_token": "tok"}, h, newScheduler(t))
	if err != nil {
		t.Fatal(err)
	}
	return ad
}

func roomEvent(t *testing.T, id, sender, body string, extra map[string]any) map[string]any {
	t.Helper()
	content := map[string]any{"msgtype": "m.text", "body": body}
	maps.Copy(content, extra)
	return map[string]any{
		"type": "m.room.message", "sender": sender, "event_id": id, "content": content,
	}
}

func syncPayload(t *testing.T, events map[string][]map[string]any, toDevice []map[string]any) string {
	t.Helper()
	join := map[string]any{}
	for room, list := range events {
		join[room] = map[string]any{"timeline": map[string]any{"events": list}}
	}
	raw, err := json.Marshal(map[string]any{
		"next_batch": "s1",
		"to_device":  map[string]any{"events": toDevice},
		"rooms":      map[string]any{"join": join},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// fakeCrypto is the E2EE seam: it returns a canned plaintext and records
// to-device events.
type fakeCrypto struct {
	mu       sync.Mutex
	plain    []byte
	toDevice int
	rooms    []string
}

func (f *fakeCrypto) DecryptRoom(_ context.Context, roomID string, _ json.RawMessage) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rooms = append(f.rooms, roomID)
	return f.plain, nil
}

func (f *fakeCrypto) EncryptRoom(context.Context, string, []byte) (json.RawMessage, error) {
	return json.RawMessage(`{"algorithm":"m.megolm.v1.aes-sha2"}`), nil
}

func (f *fakeCrypto) HandleToDevice(context.Context, string, json.RawMessage) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.toDevice++
	return true, nil
}

func (f *fakeCrypto) DeviceKeys(context.Context) (json.RawMessage, map[string]string, error) {
	return json.RawMessage(`{}`), map[string]string{}, nil
}

func (f *fakeCrypto) MarkPublished() {}

func (f *fakeCrypto) NewOutboundRoom(_ context.Context, roomID string) (*e2ee.RoomKey, error) {
	return &e2ee.RoomKey{RoomID: roomID, SessionID: "s1", SessionKey: "k1"}, nil
}

func (f *fakeCrypto) AddRoomKey(context.Context, string, *e2ee.RoomKey) error { return nil }

func (f *fakeCrypto) EncryptToDevice(context.Context, *e2ee.Device, []byte) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

// TestMatrixClientPlaintext proves the sync/send surface before E2EE: DM vs
// group mapping, mentions, threads, dedupe, own events, encrypted events with
// no E2EE, and the no-edit send shape.
func TestMatrixClientPlaintext(t *testing.T) {
	t.Run("activation_gate", testMatrixActivation)
	t.Run("sync_and_send", testMatrixSyncAndSend)
	t.Run("auth_failure", testMatrixAuthFailure)
}

func testMatrixActivation(t *testing.T) {
	srv := newFakeHomeserver(t, `{"next_batch":"s1"}`)
	cfg := &Config{Enabled: true, Homeserver: srv.srv.URL, HTTPClient: netx.LoopbackClient()}
	if _, err := buildAdapter(t, cfg, secrets{}, newRecorder(), newScheduler(t)); !errors.Is(err, gwplat.ErrMissingSecret) {
		t.Fatalf("missing secret = %v, want ErrMissingSecret", err)
	}
	if _, _, _, requests := srv.snapshot(); requests != 0 {
		t.Fatalf("missing secret made %d requests", requests)
	}
	disabled := &Config{Enabled: false, HTTPClient: netx.LoopbackClient()}
	if _, err := buildAdapter(t, disabled, secrets{"matrix_access_token": "tok"}, newRecorder(), newScheduler(t)); !errors.Is(err, gwplat.ErrUnavailable) {
		t.Fatalf("disabled = %v, want module_unavailable", err)
	}
	if err := validateHomeserver("http://example.com"); !errors.Is(err, ErrConfig) {
		t.Fatalf("plaintext remote homeserver accepted: %v", err)
	}
}

func testMatrixSyncAndSend(t *testing.T) {
	events := map[string][]map[string]any{
		"!dm:example.test": {
			roomEvent(t, "$e1", "@alice:example.test", "hello", nil),
			roomEvent(t, "$e1", "@alice:example.test", "hello", nil), // replay
			roomEvent(t, "$self", "@bot:example.test", "echo", nil),
			{
				"type": "m.room.encrypted", "sender": "@alice:example.test", "event_id": "$enc",
				"content": map[string]any{"algorithm": "m.megolm.v1.aes-sha2", "ciphertext": "x"},
			},
		},
		"!group:example.test": {
			roomEvent(t, "$e3", "@bob:example.test", "no mention", nil),
			roomEvent(t, "$e4", "@bob:example.test", "@bot:example.test ping", map[string]any{
				"m.mentions":   map[string]any{"user_ids": []string{"@bot:example.test"}},
				"m.relates_to": map[string]any{"rel_type": "m.thread", "event_id": "$root"},
			}),
		},
	}
	srv := newFakeHomeserver(t, syncPayload(t, events, nil))
	rec := newRecorder()
	ad := mustAdapter(t, srv, rec, nil)
	if err := ad.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := collectInbounds(t, rec)
	if len(got) != 2 {
		t.Fatalf("turns = %d, want 2 (replay/own/encrypted-only skipped): %+v", len(got), got)
	}
	dm := got["matrix:$e1"]
	if dm.ChatID != "!dm:example.test" || dm.Group || dm.Text != "hello" {
		t.Fatalf("dm inbound = %+v", dm)
	}
	group := got["matrix:$e4"]
	if group.ChatID != "!group:example.test" || !group.Group || !group.Mentioned || group.ThreadID != "$root" {
		t.Fatalf("group inbound = %+v", group)
	}
	if ad.since != "s1" {
		t.Fatalf("since = %q", ad.since)
	}
	assertMatrixSend(t, ad, srv)
}

func collectInbounds(t *testing.T, rec *recorder) map[string]gateway.Inbound {
	t.Helper()
	out := map[string]gateway.Inbound{}
	for {
		select {
		case in := <-rec.seen:
			out[in.UpdateID] = in
		case <-time.After(200 * time.Millisecond):
			return out
		}
	}
}

func assertMatrixSend(t *testing.T, ad *Adapter, srv *fakeHomeserver) {
	t.Helper()
	if err := ad.Send(t.Context(), "!dm:example.test", "hello **world**"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(sentOf(srv)) == 0 && time.Now().Before(deadline) {
		select {
		case <-t.Context().Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
	sent := sentOf(srv)
	if len(sent) != 1 || sent[0]["msgtype"] != "m.text" || !strings.Contains(sent[0]["body"], "world") {
		t.Fatalf("sent = %v", sent)
	}
	if _, ok := sent[0]["m.relates_to"]; ok {
		t.Fatalf("send carried an edit relation: %v", sent[0])
	}
	paths, auth, _, _ := srv.snapshot()
	if len(auth) == 0 || auth[0] != "Bearer tok" {
		t.Fatalf("auth header = %v", auth)
	}
	for _, path := range paths {
		if strings.Contains(path, "/send/m.room.message/") && !strings.HasSuffix(path, "/send/m.room.message/damnbot1") {
			t.Fatalf("send path = %q", path)
		}
	}
}

func sentOf(srv *fakeHomeserver) []map[string]string {
	_, _, sent, _ := srv.snapshot()
	return sent
}

func testMatrixAuthFailure(t *testing.T) {
	srv := newFakeHomeserver(t, `{"next_batch":"s1"}`)
	srv.setStatus(http.StatusUnauthorized)
	ad := mustAdapter(t, srv, newRecorder(), nil)
	err := ad.Run(t.Context())
	if !errors.Is(err, ErrNeedsAttention) || !errors.Is(err, gwplat.ErrUnavailable) {
		t.Fatalf("401 = %v, want needs_attention + module_unavailable", err)
	}
}

// TestMatrixEncryptedSeam proves the E2EE seam: encrypted events are decrypted
// through it and to-device events are handed over.
func TestMatrixEncryptedSeam(t *testing.T) {
	plain := roomEvent(t, "$inner", "@alice:example.test", "secret hello", nil)
	raw, err := json.Marshal(map[string]any{"type": "m.room.message", "content": plain["content"]})
	if err != nil {
		t.Fatal(err)
	}
	events := map[string][]map[string]any{
		"!dm:example.test": {
			{
				"type": "m.room.encrypted", "sender": "@alice:example.test", "event_id": "$enc",
				"content": map[string]any{"algorithm": "m.megolm.v1.aes-sha2", "ciphertext": "x"},
			},
		},
	}
	toDevice := []map[string]any{{"type": "m.room.encrypted", "sender": "@alice:example.test", "content": map[string]any{"algorithm": "m.olm.v1.curve25519-aes-sha2"}}}
	srv := newFakeHomeserver(t, syncPayload(t, events, toDevice))
	crypto := &fakeCrypto{plain: raw}
	rec := newRecorder()
	ad := mustAdapter(t, srv, rec, crypto)
	if err := ad.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case in := <-rec.seen:
		if in.Text != "secret hello" {
			t.Fatalf("encrypted inbound = %+v", in)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no decrypted inbound")
	}
	crypto.mu.Lock()
	defer crypto.mu.Unlock()
	if crypto.toDevice != 1 || len(crypto.rooms) != 1 {
		t.Fatalf("seam calls = to-device %d rooms %v", crypto.toDevice, crypto.rooms)
	}
}

// TestF7MatrixNoEditStream locks the descriptor: Matrix has no edit stream and
// the adapter exposes no edit path.
func TestF7MatrixNoEditStream(t *testing.T) {
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup("matrix")
	if err != nil {
		t.Fatal(err)
	}
	if desc.Caps.SupportsEditStream() || desc.Caps.MaxEditsPerMessage != 0 {
		t.Fatalf("matrix caps declare an edit stream: %+v", desc.Caps)
	}
	if desc.Render != "matrix" {
		t.Fatalf("renderer = %q, want matrix", desc.Render)
	}
}
