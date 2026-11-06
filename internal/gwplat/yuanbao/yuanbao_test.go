package yuanbao

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/outq"
	"github.com/damnbot/damnbot/internal/gwplat"
)

type secrets map[string]string

func (s secrets) Get(_ context.Context, name string) (string, error) {
	value, ok := s[name]
	if !ok {
		return "", errors.New("missing")
	}
	return value, nil
}

type allowPacer struct{}

func (allowPacer) TakeChat(string, string, bool, time.Time) (bool, time.Duration) { return true, 0 }
func (allowPacer) Penalize(string, time.Time)                                     {}

type recorder struct {
	in chan gateway.Inbound
}

func (r *recorder) Handle(_ context.Context, in *gateway.Inbound) error {
	r.in <- *in
	return nil
}

// fakeServer speaks the Yuanbao bot handshake and records client frames.
type fakeServer struct {
	t          *testing.T
	srv        *httptest.Server
	auth       chan Frame
	fromClient chan Frame
	mu         sync.Mutex
	conn       *websocket.Conn
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{t: t, auth: make(chan Frame, 1), fromClient: make(chan Frame, 16)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) serve(rw http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(rw, r, nil)
	if err != nil {
		return
	}
	f.mu.Lock()
	f.conn = conn
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.conn = nil
		f.mu.Unlock()
		if cerr := conn.CloseNow(); cerr != nil {
			return
		}
	}()
	ctx := r.Context()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		return
	}
	var auth Frame
	if err := json.Unmarshal(raw, &auth); err != nil {
		return
	}
	f.auth <- auth
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"auth_ok"}`)); err != nil {
		return
	}
	for {
		_, msg, rerr := conn.Read(ctx)
		if rerr != nil {
			return
		}
		var frame Frame
		if err := json.Unmarshal(msg, &frame); err != nil {
			continue
		}
		f.fromClient <- frame
	}
}

func (f *fakeServer) push(t *testing.T, frame *Frame) {
	t.Helper()
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	if conn == nil {
		t.Fatal("fake server has no connection")
	}
	if err := conn.Write(t.Context(), websocket.MessageText, raw); err != nil {
		t.Fatalf("push: %v", err)
	}
}

func waitFrame(t *testing.T, ch chan Frame) Frame {
	t.Helper()
	select {
	case f := <-ch:
		return f
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a frame")
		return Frame{}
	}
}

func waitInbound(t *testing.T, ch chan gateway.Inbound) gateway.Inbound {
	t.Helper()
	select {
	case in := <-ch:
		return in
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for an inbound message")
		return gateway.Inbound{}
	}
}

func testAdapter(t *testing.T, f *fakeServer, cfg *Config) (*Adapter, *recorder) {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup("yuanbao")
	if err != nil {
		t.Fatal(err)
	}
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
	out, err := gwplat.NewOutbox(&desc, sched)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{in: make(chan gateway.Inbound, 16)}
	cfg.Enabled = true
	cfg.WSURL = "ws://" + strings.TrimPrefix(f.srv.URL, "http://")
	cfg.AppID = "app-1"
	cfg.Secrets = secrets{"yuanbao_app_secret": "top-secret-value"}
	if cfg.DMAllowlist == nil {
		cfg.DMAllowlist = []string{"u1"}
	}
	if cfg.Stickers == nil {
		cfg.Stickers = []Sticker{{ID: "s1", Keywords: []string{"hi", "wave"}}}
	}
	ad, err := New(ctx, cfg, &desc, out, rec)
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	go func() {
		if runErr := ad.Run(ctx); runErr != nil {
			t.Logf("adapter run: %v", runErr)
		}
	}()
	auth := waitFrame(t, f.auth)
	if auth.AppID != "app-1" || auth.AppSecret != "top-secret-value" {
		t.Fatalf("auth frame = %+v", auth)
	}
	return ad, rec
}

// TestF7PLAT027Yuanbao is the PLAT-027 acceptance: explicit activation,
// secret from the seam, group/DM mapping, dedupe, cached metadata and
// allowlisted outbound.
func TestF7PLAT027Yuanbao(t *testing.T) {
	t.Parallel()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup("yuanbao")
	if err != nil {
		t.Fatal(err)
	}
	assertYuanbaoActivation(t, &desc)
	f := newFakeServer(t)
	ad, rec := testAdapter(t, f, &Config{})
	assertYuanbaoInbound(t, f, rec)
	assertYuanbaoCache(t, ad)
	assertYuanbaoSend(t, f, ad)
}

func assertYuanbaoActivation(t *testing.T, desc *gwplat.Descriptor) {
	t.Helper()
	if _, nerr := New(t.Context(), &Config{Enabled: false}, desc, nil, nil); !errors.Is(nerr, gwplat.ErrUnavailable) {
		t.Fatalf("disabled = %v, want module_unavailable", nerr)
	}
	if _, nerr := New(t.Context(), &Config{Enabled: true, WSURL: "ws://127.0.0.1:1", AppID: "app"}, desc, nil, nil); !errors.Is(nerr, gwplat.ErrUnavailable) {
		t.Fatalf("missing secret = %v, want module_unavailable", nerr)
	}
	if _, nerr := New(t.Context(), &Config{Enabled: true, WSURL: "ws://example.test/x", AppID: "app", Secrets: secrets{}}, desc, nil, nil); !errors.Is(nerr, ErrConfig) {
		t.Fatalf("public ws = %v, want ErrConfig", nerr)
	}
}

func assertYuanbaoInbound(t *testing.T, f *fakeServer, rec *recorder) {
	t.Helper()
	f.push(t, &Frame{
		Type: "message", EventID: "e1", ChatType: "group", ChatID: "g1", ChatName: "Team",
		SenderID: "u1", Text: "halo", Mentioned: true, Members: []Member{{ID: "u1", Name: "Ana"}},
	})
	in := waitInbound(t, rec.in)
	if !in.Group || in.ChatID != "g1" || in.UpdateID != "yuanbao:e1" || in.Text != "halo" || !in.Mentioned {
		t.Fatalf("group inbound = %+v", in)
	}
	f.push(t, &Frame{Type: "message", EventID: "e1", ChatType: "group", ChatID: "g1", SenderID: "u1", Text: "replay", Mentioned: true})
	select {
	case dup := <-rec.in:
		t.Fatalf("replay dispatched: %+v", dup)
	case <-time.After(150 * time.Millisecond):
	}
	f.push(t, &Frame{Type: "message", EventID: "e2", ChatType: "dm", ChatID: "u1", SenderID: "u1", Text: "dm"})
	if dm := waitInbound(t, rec.in); dm.Group || dm.ChatID != "u1" {
		t.Fatalf("dm inbound = %+v", dm)
	}
}

func assertYuanbaoCache(t *testing.T, ad *Adapter) {
	t.Helper()
	group, err := ad.GroupInfo("g1")
	if err != nil || group.Name != "Team" || len(group.Members) != 1 {
		t.Fatalf("group = %+v, %v", group, err)
	}
	if members, merr := ad.GroupMembers("g1"); merr != nil || len(members) != 1 || members[0].ID != "u1" {
		t.Fatalf("members = %+v, %v", members, merr)
	}
	if _, err := ad.GroupInfo("nope"); !errors.Is(err, ErrGroupUnknown) {
		t.Fatalf("unknown group = %v", err)
	}
}

func assertYuanbaoSend(t *testing.T, f *fakeServer, ad *Adapter) {
	t.Helper()
	if err := ad.Reply(t.Context(), "g1", true, "hello **world**"); err != nil {
		t.Fatal(err)
	}
	sent := waitFrame(t, f.fromClient)
	if sent.Type != "send" || sent.ChatID != "g1" || sent.Text == "" {
		t.Fatalf("reply frame = %+v", sent)
	}
	if err := ad.SendDM(t.Context(), "u1", "hi"); err != nil {
		t.Fatal(err)
	}
	if dm := waitFrame(t, f.fromClient); dm.ChatID != "dm:u1" {
		t.Fatalf("dm send = %+v", dm)
	}
	if err := ad.SendDM(t.Context(), "stranger", "no"); !errors.Is(err, ErrDMNotAllowed) {
		t.Fatalf("non-allowlisted DM = %v", err)
	}
	if err := ad.SendSticker(t.Context(), "g1", "s1"); err != nil {
		t.Fatal(err)
	}
	if st := waitFrame(t, f.fromClient); st.Type != "sticker" || st.StickerID != "s1" {
		t.Fatalf("sticker = %+v", st)
	}
	if err := ad.SendSticker(t.Context(), "g1", "foreign"); !errors.Is(err, ErrStickerUnknown) {
		t.Fatalf("foreign sticker = %v", err)
	}
	if got := ad.SearchSticker("wave"); len(got) != 1 || got[0].ID != "s1" {
		t.Fatalf("search = %+v", got)
	}
	if got := ad.SearchSticker("nope"); len(got) != 0 {
		t.Fatalf("search miss = %+v", got)
	}
}
