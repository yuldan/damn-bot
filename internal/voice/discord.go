// Discord voice (MEDIA-029, Q7): join/leave a voice channel, mix 48 kHz PCM16
// frames, and run the F6 voice pipeline. The opus codec is an external binary
// (ADR-001): without it construction fails closed with module_unavailable and
// no join is attempted.
package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// ErrNoOpus is the fail-closed codec gate of MEDIA-029.
var ErrNoOpus = errors.New("voice: opus/ffmpeg codec is not installed: module_unavailable{discord_voice_no_opus}")

// ErrNotJoined rejects audio work before Join.
var ErrNotJoined = errors.New("voice: discord voice is not joined")

// ErrBarged marks a play loop cancelled by barge-in.
var ErrBarged = errors.New("voice: barged in")

// Codec is the opus seam; the production implementation runs the external
// ffmpeg binary (no audio library is linked).
type Codec interface {
	DecodeOpus(frame []byte) ([]int16, error)
	EncodeOpus(pcm []int16) ([]byte, error)
}

// DiscordConfig is the explicit activation of one voice channel.
type DiscordConfig struct {
	WSURL      string
	HTTPClient *http.Client
	GuildID    string
	ChannelID  string
	UserID     string
	SessionID  string
	Token      string
	Codec      Codec
	Pipeline   *Pipeline
}

// DiscordVoice is one joined voice channel.
type DiscordVoice struct {
	cfg DiscordConfig

	mu     sync.Mutex
	conn   *websocket.Conn
	joined bool
	cancel context.CancelFunc
	stream io.Closer
	ended  chan struct{}
}

// NewDiscordVoice validates the config and the codec without dialing.
func NewDiscordVoice(cfg *DiscordConfig) (*DiscordVoice, error) {
	if cfg == nil || cfg.GuildID == "" || cfg.ChannelID == "" || cfg.UserID == "" || cfg.SessionID == "" || cfg.Token == "" {
		return nil, fmt.Errorf("%w: voice channel config", ErrPipelineNotConfigured)
	}
	if cfg.Codec == nil || cfg.Pipeline == nil {
		return nil, ErrNoOpus
	}
	if err := validateVoiceURL(cfg.WSURL); err != nil {
		return nil, err
	}
	return &DiscordVoice{cfg: *cfg}, nil
}

func validateVoiceURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("%w: voice gateway URL", ErrPipelineNotConfigured)
	}
	if u.Scheme == "wss" {
		return nil
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if u.Scheme == "ws" && (strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("%w: voice gateway must be wss or loopback ws", ErrPipelineNotConfigured)
}

// Join opens the voice WebSocket and waits for op 2 (session description).
func (v *DiscordVoice) Join(ctx context.Context) error {
	client := v.cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	conn, resp, err := websocket.Dial(ctx, v.cfg.WSURL, &websocket.DialOptions{HTTPClient: client})
	if resp != nil && resp.Body != nil {
		defer func() {
			if cerr := resp.Body.Close(); cerr != nil {
				return
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("voice: voice gateway dial: %w", err)
	}
	identify, err := json.Marshal(map[string]any{"op": 0, "d": map[string]any{
		"server_id": v.cfg.GuildID, "user_id": v.cfg.UserID,
		"session_id": v.cfg.SessionID, "token": v.cfg.Token,
	}})
	if err != nil {
		return fmt.Errorf("voice: identify: %w", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, identify); err != nil {
		return fmt.Errorf("voice: voice identify: %w", err)
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("voice: voice handshake: %w", err)
		}
		var frame struct {
			Op int `json:"op"`
		}
		if json.Unmarshal(data, &frame) != nil {
			continue
		}
		if frame.Op == 2 {
			break
		}
	}
	v.mu.Lock()
	v.conn, v.joined = conn, true
	v.mu.Unlock()
	return nil
}

// Leave closes the voice connection; a second Leave is a no-op.
func (v *DiscordVoice) Leave() error {
	v.mu.Lock()
	conn, joined := v.conn, v.joined
	v.conn, v.joined = nil, false
	v.mu.Unlock()
	if !joined || conn == nil {
		return nil
	}
	if err := conn.Close(websocket.StatusNormalClosure, "leave"); err != nil {
		return fmt.Errorf("voice: leave: %w", err)
	}
	return nil
}

// Joined reports whether the channel handshake completed.
func (v *DiscordVoice) Joined() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.joined
}

// Mix sums 48 kHz PCM16 frames and clips to the int16 range.
func (v *DiscordVoice) Mix(frames [][]int16) []int16 {
	size := 0
	for _, frame := range frames {
		if len(frame) > size {
			size = len(frame)
		}
	}
	out := make([]int16, size)
	for _, frame := range frames {
		for i, sample := range frame {
			sum := int32(out[i]) + int32(sample)
			switch {
			case sum > 32767:
				out[i] = 32767
			case sum < -32768:
				out[i] = -32768
			default:
				out[i] = int16(sum)
			}
		}
	}
	return out
}

// Turn runs the F6 pipeline on captured audio (STT → agent → TTS).
func (v *DiscordVoice) Turn(ctx context.Context, capture io.Reader) (Result, error) {
	return v.cfg.Pipeline.Turn(ctx, capture, TranscriptOptions{})
}

// Play encodes PCM16 and writes it until the track ends, the context ends, or
// barge-in cancels it and closes the stream.
func (v *DiscordVoice) Play(ctx context.Context, pcm []int16, track io.WriteCloser) error {
	if !v.Joined() {
		return ErrNotJoined
	}
	runCtx, cancel := context.WithCancel(ctx)
	v.mu.Lock()
	v.cancel, v.stream = cancel, track
	ended := make(chan struct{})
	v.ended = ended
	v.mu.Unlock()
	defer func() {
		cancel()
		v.mu.Lock()
		v.cancel, v.stream = nil, nil
		v.mu.Unlock()
		close(ended)
	}()
	frame, err := v.cfg.Codec.EncodeOpus(pcm)
	if err != nil {
		return fmt.Errorf("voice: encode: %w", err)
	}
	select {
	case <-runCtx.Done():
		return ErrBarged
	default:
	}
	if _, err := track.Write(frame); err != nil {
		if errors.Is(err, io.ErrClosedPipe) {
			return ErrBarged
		}
		return fmt.Errorf("voice: track: %w", err)
	}
	return nil
}

// BargeIn cancels the active playback and closes its stream.
func (v *DiscordVoice) BargeIn() error {
	v.mu.Lock()
	cancel, stream := v.cancel, v.stream
	v.mu.Unlock()
	if cancel == nil {
		return ErrNoActiveTurn
	}
	cancel()
	if stream != nil {
		if err := stream.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			return fmt.Errorf("voice: barge-in close: %w", err)
		}
	}
	return nil
}
