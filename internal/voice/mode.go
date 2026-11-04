package voice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/damnbot/damnbot/internal/voice/wake"
)

// Voice-mode failures (MEDIA-001/002/003).
var (
	// ErrNoCapture is a push-to-talk turn without an audio device: the turn
	// fails closed instead of opening a fake or silent source.
	ErrNoCapture = errors.New("voice: no audio capture device")
	// ErrCapture is a device that could not open its track.
	ErrCapture = errors.New("voice: audio capture failed")
	// ErrTurnActive is a second push while one turn still runs.
	ErrTurnActive = errors.New("voice: a turn is already active")
	// ErrNoActiveTurn is barge-in without a turn to interrupt.
	ErrNoActiveTurn = errors.New("voice: no active turn")
	// ErrInterrupted marks a turn stopped by barge-in.
	ErrInterrupted = errors.New("voice: turn interrupted")
	// ErrUnknownChatMode refuses a chat mode outside the closed set.
	ErrUnknownChatMode = errors.New("voice: unknown chat mode")
	// ErrLiveNotConfigured refuses gpt_live without the explicit opt-in.
	ErrLiveNotConfigured = errors.New("voice: gpt_live requires live_endpoint and live_key_env")
)

// Chat modes of MEDIA-002. Chained is the default: STT → agent → TTS.
const (
	ModeChained = "chained"
	ModeGPTLive = "gpt_live"
)

// Capture is one push-to-talk audio source. Implementations are external
// executables (ADR-001): no audio library is linked into the binary.
type Capture interface {
	Open(ctx context.Context) (io.ReadCloser, error)
}

// Mode is the voice runtime behind one push-to-talk key: capture → STT →
// agent → TTS, with barge-in and an explicit realtime opt-in. It owns no
// process; a turn is one bounded call.
type Mode struct {
	Pipeline *Pipeline
	Capture  Capture
	Wake     WakeEngine
	Live     *LiveConfig
	Chat     string

	mu      sync.Mutex
	cancel  context.CancelFunc
	barged  bool
	running bool
	wg      sync.WaitGroup
	session atomic.Uint64
}

// NewMode validates the chat mode. An empty Chat selects chained; gpt_live
// is refused without its explicit LiveConfig (never a silent default).
func NewMode(chat string, p *Pipeline, c Capture, live *LiveConfig) (*Mode, error) {
	switch chat {
	case "", ModeChained:
		chat = ModeChained
	case ModeGPTLive:
		if live == nil || live.Endpoint == "" {
			return nil, ErrLiveNotConfigured
		}
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownChatMode, chat)
	}
	return &Mode{Pipeline: p, Capture: c, Live: live, Chat: chat}, nil
}

// Push runs one push-to-talk turn. A missing device fails closed before any
// STT, LLM or TTS call; a second concurrent push is refused.
func (m *Mode) Push(ctx context.Context, opts TranscriptOptions) (res Result, err error) {
	if m == nil || m.Pipeline == nil {
		return Result{}, ErrPipelineNotConfigured
	}
	if m.Capture == nil {
		return Result{}, ErrNoCapture
	}
	if m.Chat == ModeGPTLive && (m.Live == nil || m.Live.Endpoint == "") {
		return Result{}, ErrLiveNotConfigured
	}
	run, end, err := m.begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer end()
	stream, err := m.Capture.Open(run)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrCapture, err)
	}
	defer func() { err = errors.Join(err, stream.Close()) }()
	res, err = m.turn(run, stream, opts)
	if err != nil {
		if m.interrupted() {
			return Result{}, ErrInterrupted
		}
		return Result{}, err
	}
	res.Session = m.session.Add(1)
	return res, nil
}

// Listen waits for the wake engine and starts one new turn. The engine runs
// only after the explicit opt-in; raw audio still goes only to STT.
func (m *Mode) Listen(ctx context.Context, opts TranscriptOptions) (Result, error) {
	if m == nil || m.Wake == nil || !m.Wake.Enabled() {
		return Result{}, wake.ErrDisabled
	}
	if err := m.Wake.Wait(ctx); err != nil {
		return Result{}, fmt.Errorf("voice: wake: %w", err)
	}
	return m.Push(ctx, opts)
}

// BargeIn cancels the active turn and waits for its TTS stream to close. It
// is idempotent from the caller's point of view: no turn is also no-op.
func (m *Mode) BargeIn() error {
	if m == nil {
		return ErrNoActiveTurn
	}
	m.mu.Lock()
	cancel, running := m.cancel, m.running
	if running {
		m.barged = true
	}
	m.mu.Unlock()
	if !running {
		return ErrNoActiveTurn
	}
	cancel()
	m.wg.Wait()
	return nil
}

// begin takes the single turn slot and derives the cancellable turn context.
func (m *Mode) begin(ctx context.Context) (context.Context, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return nil, nil, ErrTurnActive
	}
	run, cancel := context.WithCancel(ctx)
	m.cancel, m.barged, m.running = cancel, false, true
	m.wg.Add(1)
	end := func() {
		cancel()
		m.mu.Lock()
		m.cancel, m.running = nil, false
		m.mu.Unlock()
		m.wg.Done()
	}
	return run, end, nil
}

func (m *Mode) interrupted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.barged
}

// turn runs the selected backend; a failed realtime session falls back to
// chained and the result carries the label.
func (m *Mode) turn(ctx context.Context, audio io.Reader, opts TranscriptOptions) (Result, error) {
	if m.Chat != ModeGPTLive {
		res, err := m.Pipeline.Turn(ctx, audio, opts)
		res.Mode = ModeChained
		return res, err
	}
	tr, err := m.Pipeline.STT.Transcribe(ctx, audio, opts)
	if err != nil {
		return Result{}, fmt.Errorf("voice: transcribe: %w", err)
	}
	if tr.Text == "" {
		return Result{}, ErrEmptyTranscript
	}
	res, liveErr := m.liveTurn(ctx, &tr)
	if liveErr == nil {
		res.Mode = ModeGPTLive
		return res, nil
	}
	if ctx.Err() != nil {
		return Result{}, liveErr
	}
	out, err := m.Pipeline.FromTranscript(ctx, &tr)
	if err != nil {
		return Result{}, errors.Join(fmt.Errorf("voice: live: %w", liveErr), err)
	}
	out.Mode, out.FallbackFrom = ModeChained, ModeGPTLive
	return out, nil
}

func (m *Mode) liveTurn(ctx context.Context, tr *Transcript) (res Result, err error) {
	live, err := DialLive(ctx, m.Live)
	if err != nil {
		return Result{}, err
	}
	defer func() { err = errors.Join(err, live.Close()) }()
	audio, reply, err := live.Say(ctx, tr.Text)
	if err != nil {
		return Result{}, err
	}
	return Result{Transcript: *tr, Reply: reply, Audio: []Audio{audio}}, nil
}
