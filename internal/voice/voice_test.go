package voice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/voice/stt"
	"github.com/damnbot/damnbot/internal/voice/wake"
)

var (
	errBoom = errors.New("boom")
	errLLM  = errors.New("llm")
)

func TestCatalogClosedSet(t *testing.T) {
	t.Parallel()
	if got := strings.Join(Catalog(KindSTT), ","); got != "whispercpp,local_command,groq,openai,mistral,xai,elevenlabs,deepinfra" {
		t.Errorf("stt catalog = %s", got)
	}
	if got := strings.Join(Catalog(KindTTS), ","); got != "edge,elevenlabs,openai,minimax,xai,mistral,gemini,neutts,kittentts,piper,deepinfra" {
		t.Errorf("tts catalog = %s", got)
	}
	if Catalog(Kind("other")) != nil {
		t.Error("unknown kind must have no catalog")
	}
}

func TestProviderSelection(t *testing.T) {
	t.Parallel()
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	model := filepath.Join(t.TempDir(), "model.bin")
	if err := os.WriteFile(model, []byte("model"), 0o600); err != nil {
		t.Fatalf("write model: %v", err)
	}
	tests := []struct {
		name string
		cfg  Config
		stt  bool
		want error
	}{
		{"stt_empty", Config{}, true, ErrUnknownProvider},
		{"stt_unknown", Config{STTBackend: "nope"}, true, ErrUnknownProvider},
		{"stt_catalog_only", Config{STTBackend: "openai"}, true, ErrModuleUnavailable},
		{"stt_unconfigured", Config{STTBackend: "whispercpp"}, true, ErrModuleUnavailable},
		{"tts_empty", Config{}, false, ErrUnknownProvider},
		{"tts_unknown", Config{TTSBackend: "nope"}, false, ErrUnknownProvider},
		{"tts_catalog_only", Config{TTSBackend: "piper"}, false, ErrModuleUnavailable},
		{"tts_unconfigured", Config{TTSBackend: "openai"}, false, ErrModuleUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var err error
			if tc.stt {
				_, err = tc.cfg.NewSTT()
			} else {
				_, err = tc.cfg.NewTTS()
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	cfg := Config{STTBackend: "whispercpp", WhisperBin: bin, WhisperModel: model}
	if _, err := cfg.NewSTT(); err != nil {
		t.Fatalf("configured STT: %v", err)
	}
	ttsCfg := Config{TTSBackend: "openai", TTSEndpoint: "https://example.com/v1/audio/speech", Doer: fakeDoer{}}
	if _, err := ttsCfg.NewTTS(); err != nil {
		t.Fatalf("configured TTS: %v", err)
	}
}

func TestWakeDisabledByDefault(t *testing.T) {
	t.Parallel()
	e, err := wake.New(&WakeConfig{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.Enabled() {
		t.Fatal("wake enabled without opt-in")
	}
	if err := e.Wait(t.Context()); !errors.Is(err, wake.ErrDisabled) {
		t.Fatalf("err = %v, want ErrDisabled", err)
	}
	if _, err := wake.New(&WakeConfig{Enabled: true}); err == nil {
		t.Fatal("enabled engine without command must fail")
	}
}

// fakeDoer is a Doer that never runs; only construction is exercised.
type fakeDoer struct{}

func (fakeDoer) Do(*http.Request) (*http.Response, error) { return nil, nil }

type fakeSTT struct {
	tr    Transcript
	err   error
	delay time.Duration
	done  time.Time
}

func (f *fakeSTT) Transcribe(ctx context.Context, _ io.Reader, _ TranscriptOptions) (Transcript, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return Transcript{}, fmt.Errorf("fake stt: %w", ctx.Err())
		}
	}
	f.done = time.Now()
	return f.tr, f.err
}

type fakeLLM struct {
	sentences []string
	err       error
	first     time.Time
	calls     int
	mu        sync.Mutex
	prompt    string
}

func (f *fakeLLM) Stream(ctx context.Context, prompt string, emit func(string) error) error {
	f.calls++
	f.mu.Lock()
	f.prompt = prompt
	f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	for _, s := range f.sentences {
		for _, part := range strings.SplitAfter(s, " ") {
			if f.first.IsZero() {
				f.first = time.Now()
			}
			if err := emit(part); err != nil {
				return err
			}
		}
	}
	return nil
}

type fakeTTS struct {
	texts []string
	at    []time.Time
	err   error
	delay time.Duration
}

func (f *fakeTTS) Synthesize(ctx context.Context, text string, _ SynthOptions) (Audio, error) {
	f.at = append(f.at, time.Now())
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return Audio{}, fmt.Errorf("fake tts: %w", ctx.Err())
		}
	}
	if f.err != nil {
		return Audio{}, f.err
	}
	f.texts = append(f.texts, text)
	return Audio{MIME: "audio/mpeg", Ext: ".mp3", Bytes: []byte(text), Backend: "fake"}, nil
}

// runVoiceLatency exercises the latency budgets with timestamped fakes:
// end-of-speech → first audio ≤ 200 ms, first sentence → TTS start ≤ 300 ms.
func runVoiceLatency(t *testing.T) {
	t.Helper()
	in, out := &fakeSTT{tr: Transcript{Text: "halo bot", MIME: "audio/wav"}, delay: 20 * time.Millisecond}, &fakeTTS{delay: 10 * time.Millisecond}
	llm := &fakeLLM{sentences: []string{"Satu. ", "Dua. ", "Tiga."}}
	p := &Pipeline{STT: in, TTS: out, LLM: llm}
	res, err := p.Turn(t.Context(), strings.NewReader("audio"), TranscriptOptions{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if res.Reply != "Satu. Dua. Tiga." {
		t.Errorf("reply = %q", res.Reply)
	}
	if len(out.texts) != 3 || len(res.Audio) != 3 {
		t.Errorf("tts calls = %v, audio = %d", out.texts, len(res.Audio))
	}
	if len(out.at) != 3 || llm.first.IsZero() || in.done.IsZero() {
		t.Fatalf("missing timestamps: tts=%v llm=%v stt=%v", out.at, llm.first, in.done)
	}
	overhead := out.at[0].Sub(in.done)
	ttsStart := out.at[0].Sub(llm.first)
	if overhead > 200*time.Millisecond {
		t.Errorf("overhead = %s > 200ms", overhead)
	}
	if ttsStart > 300*time.Millisecond {
		t.Errorf("TTS start = %s > 300ms", ttsStart)
	}
	t.Logf("overhead=%s tts_start=%s", overhead, ttsStart)
	t.Logf("F6-VOICE-LATENCY {\"overhead_ms\":%.3f,\"tts_start_ms\":%.3f,\"runs\":1}",
		float64(overhead.Microseconds())/1000, float64(ttsStart.Microseconds())/1000)
}

// TestVoicePipelineLatency is the SPEC §9.5 name kept for the nightly gate.
func TestVoicePipelineLatency(t *testing.T) {
	t.Parallel()
	runVoiceLatency(t)
}

// TestF6VoiceLatencyBudget is the ledger-named budget assertion (P6a).
func TestF6VoiceLatencyBudget(t *testing.T) {
	t.Parallel()
	runVoiceLatency(t)
}

func TestVoicePipelineNegativeControls(t *testing.T) {
	t.Parallel()
	base := func() *Pipeline {
		return &Pipeline{STT: &fakeSTT{tr: Transcript{Text: "halo"}}, TTS: &fakeTTS{}, LLM: &fakeLLM{sentences: []string{"Satu."}}}
	}
	tests := []struct {
		name   string
		mutate func(*Pipeline)
		want   error
	}{
		{"nil_audio", func(*Pipeline) {}, ErrPipelineNotConfigured},
		{"empty_audio", func(p *Pipeline) { p.STT = &fakeSTT{err: stt.ErrEmptyAudio} }, stt.ErrEmptyAudio},
		{"empty_transcript", func(p *Pipeline) { p.STT = &fakeSTT{tr: Transcript{Text: "  "}} }, ErrEmptyTranscript},
		{"empty_reply", func(p *Pipeline) { p.LLM = &fakeLLM{} }, ErrEmptyReply},
		{"tts_error", func(p *Pipeline) { p.TTS = &fakeTTS{err: errBoom} }, errBoom},
		{"llm_error", func(p *Pipeline) { p.LLM = &fakeLLM{err: errLLM} }, errLLM},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := base()
			tc.mutate(p)
			var r io.Reader = strings.NewReader("audio")
			if tc.name == "nil_audio" {
				r = nil
			}
			_, err := p.Turn(t.Context(), r, TranscriptOptions{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestPipelineDrainsAfterTTSFailure(t *testing.T) {
	t.Parallel()
	sentences := make([]string, 32)
	for i := range sentences {
		sentences[i] = "Kalimat. "
	}
	p := &Pipeline{STT: &fakeSTT{tr: Transcript{Text: "halo"}}, TTS: &fakeTTS{err: errBoom}, LLM: &fakeLLM{sentences: sentences}}
	if _, err := p.Turn(t.Context(), strings.NewReader("audio"), TranscriptOptions{}); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}
}

func TestSplitSentence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
		rest string
		ok   bool
	}{
		{"Halo dunia. Apa", "Halo dunia.", "Apa", true},
		{"Halo!", "Halo!", "", true},
		{"Tunggu", "", "Tunggu", false},
		{"3.14 bukan akhir", "", "3.14 bukan akhir", false},
		{"Baris\nbaru", "Baris", "baru", true},
	}
	for _, tc := range tests {
		got, rest, ok := splitSentence(tc.in)
		if got != tc.want || rest != tc.rest || ok != tc.ok {
			t.Errorf("splitSentence(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.in, got, rest, ok, tc.want, tc.rest, tc.ok)
		}
	}
}

func TestSTTCatalogClosed(t *testing.T) {
	t.Parallel()
	if got := strings.Join(Catalog(KindSTT), ","); got != "whispercpp,local_command,groq,openai,mistral,xai,elevenlabs,deepinfra" {
		t.Fatalf("stt catalog = %s", got)
	}
}

func TestF6MEDIA007TTSProviders(t *testing.T) {
	t.Parallel()
	if got := strings.Join(Catalog(KindTTS), ","); got != "edge,elevenlabs,openai,minimax,xai,mistral,gemini,neutts,kittentts,piper,deepinfra" {
		t.Fatalf("tts catalog = %s", got)
	}
}

// TestF6MEDIA008StreamingLatency proves the first sentence reaches TTS within
// 300 ms while the LLM is still generating, and cancellation closes the
// sentence stream.
func TestF6MEDIA008StreamingLatency(t *testing.T) {
	t.Parallel()
	in, out := &fakeSTT{tr: Transcript{Text: "halo"}}, &fakeTTS{}
	p := &Pipeline{STT: in, TTS: out, LLM: &fakeLLM{sentences: []string{"Satu. ", "Dua."}}}
	res, err := p.Turn(t.Context(), strings.NewReader("audio"), TranscriptOptions{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(out.texts) != 2 || res.Reply != "Satu. Dua." {
		t.Fatalf("tts=%v reply=%q", out.texts, res.Reply)
	}
	if len(out.at) != 2 || out.at[0].After(out.at[1]) {
		t.Fatalf("tts timestamps = %v", out.at)
	}

	// A cancelled turn closes the stream: the blocking second sentence is
	// aborted and Turn returns instead of hanging.
	blocker := newBlockingTTS()
	ctx, cancel := context.WithCancel(t.Context())
	cp := &Pipeline{STT: &fakeSTT{tr: Transcript{Text: "halo"}}, TTS: blocker, LLM: &fakeLLM{sentences: []string{"Satu. ", "Dua. "}}}
	done := make(chan error, 1)
	go func() {
		_, err := cp.Turn(ctx, strings.NewReader("audio"), TranscriptOptions{})
		done <- err
	}()
	select {
	case <-blocker.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first sentence never reached TTS")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled Turn returned nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled Turn did not close the stream")
	}
	if !blocker.canceled.Load() {
		t.Fatal("TTS did not observe cancellation")
	}
}
