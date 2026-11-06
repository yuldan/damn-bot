package voice

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/damnbot/damnbot/internal/netx"
)

type fakeVoiceGateway struct {
	srv      *httptest.Server
	conns    atomic.Int64
	identify chan map[string]any
}

func newFakeVoiceGateway(t *testing.T) *fakeVoiceGateway {
	t.Helper()
	g := &fakeVoiceGateway{identify: make(chan map[string]any, 4)}
	g.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(rw, r, nil)
		if err != nil {
			return
		}
		g.conns.Add(1)
		defer func() {
			if cerr := conn.CloseNow(); cerr != nil {
				return
			}
		}()
		_, data, rerr := conn.Read(r.Context())
		if rerr != nil {
			return
		}
		var frame struct {
			Op int            `json:"op"`
			D  map[string]any `json:"d"`
		}
		if cerr := json.Unmarshal(data, &frame); cerr != nil {
			return
		}
		g.identify <- frame.D
		ready, cerr := json.Marshal(map[string]any{"op": 2, "d": map[string]any{"ssrc": 1, "ip": "127.0.0.1", "port": 5000}})
		if cerr != nil {
			return
		}
		_ = conn.Write(r.Context(), websocket.MessageText, ready)
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeVoiceGateway) url() string { return "ws" + strings.TrimPrefix(g.srv.URL, "http") }

type fakeCodec struct{}

func (fakeCodec) DecodeOpus([]byte) ([]int16, error) { return []int16{1, 2}, nil }
func (fakeCodec) EncodeOpus([]int16) ([]byte, error) { return []byte("opus"), nil }
func (fakeCodec) EncodePCM([]int16) ([]byte, error)  { return []byte("opus"), nil }

type blockingTrack struct {
	mu     sync.Mutex
	closed bool
	done   chan struct{}
	once   sync.Once
}

func newBlockingTrack() *blockingTrack { return &blockingTrack{done: make(chan struct{})} }

func (b *blockingTrack) Write([]byte) (int, error) {
	<-b.done
	return 0, io.ErrClosedPipe
}

func (b *blockingTrack) Close() error {
	b.once.Do(func() { close(b.done) })
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

func newDiscordVoice(t *testing.T, gw *fakeVoiceGateway, codec Codec) *DiscordVoice {
	t.Helper()
	v, err := NewDiscordVoice(&DiscordConfig{
		WSURL: gw.url(), GuildID: "g1", ChannelID: "c1", UserID: "u1", SessionID: "s1", Token: "tok", HTTPClient: netx.LoopbackClient(),
		Codec:    codec,
		Pipeline: &Pipeline{STT: &fakeSTT{tr: Transcript{Text: "hello"}}, TTS: &fakeTTS{}, LLM: &fakeLLM{sentences: []string{"hi. "}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestF7MEDIA029DiscordVoice covers MEDIA-029: join/leave, the 48 kHz mixer,
// the F6 pipeline turn and barge-in closing the stream.
func TestF7MEDIA029DiscordVoice(t *testing.T) {
	t.Run("join_leave", testDiscordJoinLeave)
	t.Run("mixer", testDiscordMixer)
	t.Run("turn", testDiscordTurn)
	t.Run("barge_in", testDiscordBargeIn)
}

func testDiscordJoinLeave(t *testing.T) {
	gw := newFakeVoiceGateway(t)
	v := newDiscordVoice(t, gw, fakeCodec{})
	if err := v.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !v.Joined() {
		t.Fatal("voice channel not joined")
	}
	select {
	case identify := <-gw.identify:
		if identify["server_id"] != "g1" || identify["channel_id"] != nil {
			t.Fatalf("identify = %v", identify)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no identify frame")
	}
	if err := v.Leave(); err != nil {
		t.Fatal(err)
	}
	if v.Joined() {
		t.Fatal("still joined after leave")
	}
	if err := v.Leave(); err != nil {
		t.Fatalf("second leave = %v", err)
	}
	if gw.conns.Load() != 1 {
		t.Fatalf("gateway connections = %d", gw.conns.Load())
	}
}

func testDiscordMixer(t *testing.T) {
	v := &DiscordVoice{}
	mixed := v.Mix([][]int16{{100, -100, 32767}, {50, -200, 32767}})
	want := []int16{150, -300, 32767}
	for i, sample := range want {
		if mixed[i] != sample {
			t.Fatalf("mix[%d] = %d, want %d", i, mixed[i], sample)
		}
	}
	if len(v.Mix(nil)) != 0 || len(v.Mix([][]int16{{}})) != 0 {
		t.Fatal("empty mixer input grew output")
	}
}

func testDiscordTurn(t *testing.T) {
	gw := newFakeVoiceGateway(t)
	v := newDiscordVoice(t, gw, fakeCodec{})
	if err := v.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Leave() })
	res, err := v.Turn(t.Context(), strings.NewReader("audio"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Transcript.Text != "hello" || res.Reply != "hi." {
		t.Fatalf("turn = %+v", res)
	}
}

func testDiscordBargeIn(t *testing.T) {
	gw := newFakeVoiceGateway(t)
	v := newDiscordVoice(t, gw, fakeCodec{})
	if err := v.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Leave() })
	track := newBlockingTrack()
	done := make(chan error, 1)
	go func() { done <- v.Play(t.Context(), []int16{1, 2}, track) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		v.mu.Lock()
		active := v.cancel != nil
		v.mu.Unlock()
		if active || time.Now().After(deadline) {
			break
		}
		select {
		case <-t.Context().Done():
		case <-time.After(time.Millisecond):
		}
	}
	if err := v.BargeIn(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrBarged) {
			t.Fatalf("play after barge-in = %v, want ErrBarged", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("play did not stop after barge-in")
	}
	track.mu.Lock()
	defer track.mu.Unlock()
	if !track.closed {
		t.Fatal("barge-in did not close the stream")
	}
	if err := (&DiscordVoice{}).BargeIn(); !errors.Is(err, ErrNoActiveTurn) {
		t.Fatalf("idle barge-in = %v", err)
	}
}

// TestF7DiscordVoiceNoOpus is the Q7 negative control: without the opus codec
// construction fails closed and nothing dials.
func TestF7DiscordVoiceNoOpus(t *testing.T) {
	gw := newFakeVoiceGateway(t)
	_, err := NewDiscordVoice(&DiscordConfig{
		WSURL: gw.url(), GuildID: "g1", ChannelID: "c1", UserID: "u1", SessionID: "s1", Token: "tok", HTTPClient: netx.LoopbackClient(),
		Pipeline: &Pipeline{},
	})
	if !errors.Is(err, ErrNoOpus) || !strings.Contains(err.Error(), "module_unavailable{discord_voice_no_opus}") {
		t.Fatalf("no codec = %v, want ErrNoOpus", err)
	}
	if gw.conns.Load() != 0 {
		t.Fatalf("no-codec construction opened %d connections", gw.conns.Load())
	}
}
