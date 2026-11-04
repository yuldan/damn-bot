package voice

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeCapture is one push-to-talk track; closed records the device release.
type fakeCapture struct {
	audio  string
	err    error
	opened atomic.Int64
	closed atomic.Bool
}

func (c *fakeCapture) Open(context.Context) (io.ReadCloser, error) {
	c.opened.Add(1)
	if c.err != nil {
		return nil, c.err
	}
	return &trackReader{Reader: strings.NewReader(c.audio), closed: &c.closed}, nil
}

type trackReader struct {
	io.Reader
	closed *atomic.Bool
}

func (r *trackReader) Close() error {
	r.closed.Store(true)
	return nil
}

// blockingTTS blocks the second sentence until the turn context is cancelled.
type blockingTTS struct {
	mu       sync.Mutex
	texts    []string
	started  chan struct{}
	canceled atomic.Bool
}

func newBlockingTTS() *blockingTTS {
	return &blockingTTS{started: make(chan struct{})}
}

func (b *blockingTTS) Synthesize(ctx context.Context, text string, _ SynthOptions) (Audio, error) {
	b.mu.Lock()
	b.texts = append(b.texts, text)
	n := len(b.texts)
	b.mu.Unlock()
	if n == 1 {
		close(b.started)
		return Audio{MIME: "audio/mpeg", Ext: ".mp3", Bytes: []byte(text)}, nil
	}
	<-ctx.Done()
	b.canceled.Store(true)
	return Audio{}, fmt.Errorf("tts: %w", ctx.Err())
}

// fakeWake is one already-armed wake engine.
type fakeWake struct {
	enabled bool
	err     error
	calls   atomic.Int64
}

func (f *fakeWake) Enabled() bool { return f.enabled }

func (f *fakeWake) Wait(ctx context.Context) error {
	f.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fake wake: %w", err)
	}
	return f.err
}

func TestF6MEDIA001VoiceMode(t *testing.T) {
	t.Parallel()
	capture := &fakeCapture{audio: "RIFF....WAVE"}
	llm := &fakeLLM{sentences: []string{"Balasan pertama. "}}
	ttsOut := &fakeTTS{}
	mode, err := NewMode(ModeChained, &Pipeline{STT: &fakeSTT{tr: Transcript{Text: "halo"}}, TTS: ttsOut, LLM: llm}, capture, nil)
	if err != nil {
		t.Fatalf("NewMode: %v", err)
	}
	res, err := mode.Push(t.Context(), TranscriptOptions{})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if res.Mode != ModeChained || res.Transcript.Text != "halo" || res.Reply != "Balasan pertama." {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Audio) != 1 || res.Session != 1 || !capture.closed.Load() || capture.opened.Load() != 1 {
		t.Fatalf("audio=%d session=%d closed=%v opened=%d", len(res.Audio), res.Session, capture.closed.Load(), capture.opened.Load())
	}

	// Fail-closed: without a device nothing downstream is called.
	noDevice, nerr := NewMode(ModeChained, &Pipeline{STT: &fakeSTT{}, TTS: ttsOut, LLM: llm}, nil, nil)
	if nerr != nil {
		t.Fatalf("NewMode nil capture: %v", nerr)
	}
	if _, perr := noDevice.Push(t.Context(), TranscriptOptions{}); !errors.Is(perr, ErrNoCapture) {
		t.Fatalf("err = %v, want ErrNoCapture", perr)
	}
	if llm.calls != 1 {
		t.Fatalf("LLM called with no device: %d", llm.calls)
	}

	// A device that cannot open its track also fails closed.
	broken, berr := NewMode(ModeChained, &Pipeline{STT: &fakeSTT{}, TTS: ttsOut, LLM: llm}, &fakeCapture{err: errors.New("no microphone")}, nil)
	if berr != nil {
		t.Fatalf("NewMode broken capture: %v", berr)
	}
	if _, perr := broken.Push(t.Context(), TranscriptOptions{}); !errors.Is(perr, ErrCapture) {
		t.Fatalf("err = %v, want ErrCapture", perr)
	}
}

func TestF6MEDIA001BargeIn(t *testing.T) {
	t.Parallel()
	ttsOut := newBlockingTTS()
	capture := &fakeCapture{audio: "audio"}
	mode, err := NewMode(ModeChained, &Pipeline{
		STT: &fakeSTT{tr: Transcript{Text: "halo"}},
		TTS: ttsOut,
		LLM: &fakeLLM{sentences: []string{"Satu. ", "Dua. "}},
	}, capture, nil)
	if err != nil {
		t.Fatalf("NewMode: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := mode.Push(t.Context(), TranscriptOptions{})
		done <- err
	}()
	select {
	case <-ttsOut.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first sentence never reached TTS")
	}
	if err := mode.BargeIn(); err != nil {
		t.Fatalf("BargeIn: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrInterrupted) {
			t.Fatalf("Push err = %v, want ErrInterrupted", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("barge-in did not stop the turn")
	}
	if !ttsOut.canceled.Load() {
		t.Fatal("TTS context was not cancelled")
	}
	if !capture.closed.Load() {
		t.Fatal("capture track was not closed")
	}
	if err := mode.BargeIn(); !errors.Is(err, ErrNoActiveTurn) {
		t.Fatalf("second BargeIn err = %v, want ErrNoActiveTurn", err)
	}
}

func TestF6MEDIA002GPTLiveOptIn(t *testing.T) {
	t.Parallel()
	audio := []byte{1, 2, 3, 4}
	server := realtimeServer(t, audio)
	defer server.Close()
	cfg := &LiveConfig{Endpoint: server.URL, Key: "sk-test-secret", Client: server.Client(), Model: "gpt-4o-realtime-preview"}
	t.Run("say", func(t *testing.T) { checkLiveSay(t, cfg, audio) })
	t.Run("refused", checkLiveRefused)
	t.Run("replaces", func(t *testing.T) { checkLiveReplaces(t, cfg) })
	t.Run("fallback", checkLiveFallback)
}

func checkLiveSay(t *testing.T, cfg *LiveConfig, audio []byte) {
	t.Helper()
	live, err := DialLive(t.Context(), cfg)
	if err != nil {
		t.Fatalf("DialLive: %v", err)
	}
	clip, reply, err := live.Say(t.Context(), "halo realtime")
	if err != nil {
		t.Fatalf("Say: %v", err)
	}
	if cerr := live.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	if !bytes.Equal(clip.Bytes, audio) || clip.Backend != ModeGPTLive || clip.MIME != "audio/pcm" {
		t.Fatalf("clip = %+v", clip)
	}
	if reply != "halo realtime" {
		t.Fatalf("reply = %q", reply)
	}
}

func checkLiveRefused(t *testing.T) {
	t.Helper()
	if _, err := NewMode(ModeGPTLive, &Pipeline{}, &fakeCapture{}, nil); !errors.Is(err, ErrLiveNotConfigured) {
		t.Fatalf("NewMode err = %v, want ErrLiveNotConfigured", err)
	}
	if _, err := NewMode("autodetect", &Pipeline{}, &fakeCapture{}, nil); !errors.Is(err, ErrUnknownChatMode) {
		t.Fatalf("NewMode err = %v, want ErrUnknownChatMode", err)
	}
}

func checkLiveReplaces(t *testing.T, cfg *LiveConfig) {
	t.Helper()
	chained := &fakeTTS{}
	p := &Pipeline{STT: &fakeSTT{tr: Transcript{Text: "halo realtime"}}, TTS: chained, LLM: &fakeLLM{sentences: []string{"chained."}}}
	mode, err := NewMode(ModeGPTLive, p, &fakeCapture{audio: "audio"}, cfg)
	if err != nil {
		t.Fatalf("NewMode live: %v", err)
	}
	res, err := mode.Push(t.Context(), TranscriptOptions{})
	if err != nil {
		t.Fatalf("Push live: %v", err)
	}
	if res.Mode != ModeGPTLive || res.FallbackFrom != "" || len(res.Audio) != 1 || chained.texts != nil {
		t.Fatalf("live result = %+v chained=%v", res, chained.texts)
	}
}

func checkLiveFallback(t *testing.T) {
	t.Helper()
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	fallbackTTS := &fakeTTS{}
	fp := &Pipeline{STT: &fakeSTT{tr: Transcript{Text: "halo"}}, TTS: fallbackTTS, LLM: &fakeLLM{sentences: []string{"jatuh."}}}
	fm, err := NewMode(ModeGPTLive, fp, &fakeCapture{audio: "audio"}, &LiveConfig{Endpoint: deadURL, Client: http.DefaultClient})
	if err != nil {
		t.Fatalf("NewMode fallback: %v", err)
	}
	res, err := fm.Push(t.Context(), TranscriptOptions{})
	if err != nil {
		t.Fatalf("Push fallback: %v", err)
	}
	if res.Mode != ModeChained || res.FallbackFrom != ModeGPTLive || len(fallbackTTS.texts) != 1 {
		t.Fatalf("fallback result = %+v tts=%v", res, fallbackTTS.texts)
	}
}

// realtimeServer speaks the subset of the OpenAI realtime protocol the client
// uses and never echoes the Authorization header back.
func realtimeServer(t *testing.T, audio []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sk-") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() {
			if cerr := conn.Close(websocket.StatusNormalClosure, ""); cerr != nil {
				t.Errorf("close realtime fixture: %v", cerr)
			}
		}()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		for range 3 {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
		delta := base64.StdEncoding.EncodeToString(audio)
		for _, ev := range []string{
			`{"type":"response.audio.delta","delta":"` + delta + `"}`,
			`{"type":"response.audio_transcript.delta","delta":"halo realtime"}`,
			`{"type":"response.done","response":{"status":"completed"}}`,
		} {
			if err := conn.Write(ctx, websocket.MessageText, []byte(ev)); err != nil {
				return
			}
		}
	}))
}

func TestF6MEDIA003WakeOptIn(t *testing.T) {
	t.Parallel()
	ttsOut := &fakeTTS{}
	llm := &fakeLLM{sentences: []string{"Balasan."}}
	capture := &fakeCapture{audio: "RAW-AUDIO-BYTES"}
	mode, err := NewMode(ModeChained, &Pipeline{STT: &fakeSTT{tr: Transcript{Text: "halo"}}, TTS: ttsOut, LLM: llm}, capture, nil)
	if err != nil {
		t.Fatalf("NewMode: %v", err)
	}
	// Disabled: no listener and no device opened.
	if _, lerr := mode.Listen(t.Context(), TranscriptOptions{}); lerr == nil {
		t.Fatal("Listen ran without an engine")
	}
	if capture.opened.Load() != 0 {
		t.Fatal("capture opened without an engine")
	}
	engine := &fakeWake{enabled: true}
	mode.Wake = engine
	res, err := mode.Listen(t.Context(), TranscriptOptions{})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if engine.calls.Load() != 1 {
		t.Fatalf("engine calls = %d", engine.calls.Load())
	}
	if res.Transcript.Text != "halo" || res.Session == 0 {
		t.Fatalf("result = %+v", res)
	}
	// The model sees the transcript only, never the raw track bytes.
	if got := llm.lastPrompt(); strings.Contains(got, "RAW-AUDIO-BYTES") || got != "halo" {
		t.Fatalf("prompt = %q", got)
	}
	if capture.opened.Load() != 1 {
		t.Fatalf("capture opened %d times", capture.opened.Load())
	}
}

// lastPrompt returns the prompt of the most recent stream call.
func (f *fakeLLM) lastPrompt() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prompt
}
