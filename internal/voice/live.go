package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/damnbot/damnbot/internal/voice/tts"
)

// Realtime failures (MEDIA-002).
var (
	// ErrLive is a realtime session that could not connect or failed.
	ErrLive = errors.New("voice/live: realtime session failed")
	// ErrLiveProtocol is an event the client cannot interpret.
	ErrLiveProtocol = errors.New("voice/live: unexpected realtime event")
	// ErrLiveTooLarge is an audio response over the cap.
	ErrLiveTooLarge = errors.New("voice/live: audio response exceeds the limit")
)

const (
	// LiveMaxAudioBytes caps one realtime audio response (25 MiB).
	LiveMaxAudioBytes = 25 << 20
	// LiveTimeout bounds one connect-and-answer exchange.
	LiveTimeout = 2 * time.Minute
)

// LiveConfig is the explicit gpt_live opt-in. Endpoint is an http(s) URL
// whose scheme is upgraded to ws(s); Key must be resolved from a secret.
type LiveConfig struct {
	Endpoint, Key, Model, Voice string
	Client                      *http.Client
	Timeout                     time.Duration
	MaxBytes                    int64
}

// Live is one realtime connection. It is not safe for concurrent use.
type Live struct {
	ws      *websocket.Conn
	max     int64
	voice   string
	timeout time.Duration
}

// DialLive opens the realtime WebSocket. It fails closed without an endpoint
// or an egress client; nothing is dialled at construction time elsewhere.
func DialLive(ctx context.Context, cfg *LiveConfig) (*Live, error) {
	if cfg == nil || strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, ErrLiveNotConfigured
	}
	if cfg.Client == nil {
		return nil, fmt.Errorf("%w: no HTTP client", ErrLiveNotConfigured)
	}
	target, err := realtimeURL(cfg.Endpoint, cfg.Model)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLiveNotConfigured, err)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = LiveTimeout
	}
	max := cfg.MaxBytes
	if max <= 0 {
		max = LiveMaxAudioBytes
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	header := http.Header{"OpenAI-Beta": []string{"realtime=v1"}}
	if cfg.Key != "" {
		header.Set("Authorization", "Bearer "+cfg.Key)
	}
	ws, resp, err := websocket.Dial(cctx, target, &websocket.DialOptions{HTTPClient: cfg.Client, HTTPHeader: header})
	if resp != nil && resp.Body != nil {
		err = errors.Join(err, resp.Body.Close())
	}
	if err != nil {
		return nil, fmt.Errorf("%w: connect", ErrLive)
	}
	ws.SetReadLimit(max + (1 << 20))
	return &Live{ws: ws, max: max, voice: cfg.Voice, timeout: timeout}, nil
}

// Say sends one text turn and collects the streamed PCM16 answer.
func (l *Live) Say(ctx context.Context, text string) (tts.Audio, string, error) {
	body := strings.TrimSpace(text)
	if body == "" {
		return tts.Audio{}, "", tts.ErrEmptyText
	}
	cctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	if err := l.send(cctx, body); err != nil {
		return tts.Audio{}, "", err
	}
	var audio, reply strings.Builder
	for {
		_, raw, err := l.ws.Read(cctx)
		if err != nil {
			return tts.Audio{}, "", l.fail(err)
		}
		var ev liveEvent
		if jerr := json.Unmarshal(raw, &ev); jerr != nil {
			return tts.Audio{}, "", fmt.Errorf("%w: %w", ErrLiveProtocol, jerr)
		}
		done, err := l.apply(&ev, &audio, &reply)
		if err != nil {
			return tts.Audio{}, "", err
		}
		if !done {
			continue
		}
		if audio.Len() == 0 {
			return tts.Audio{}, "", fmt.Errorf("%w: no audio", ErrLive)
		}
		return tts.Audio{MIME: "audio/pcm", Ext: ".pcm", Bytes: []byte(audio.String()), Backend: ModeGPTLive}, strings.TrimSpace(reply.String()), nil
	}
}

// send writes the three client events of one text turn.
func (l *Live) send(ctx context.Context, body string) error {
	session := map[string]any{"modalities": []string{"audio", "text"}, "output_audio_format": "pcm16"}
	if l.voice != "" {
		session["voice"] = l.voice
	}
	events := []any{
		map[string]any{"type": "session.update", "session": session},
		map[string]any{
			"type": "conversation.item.create",
			"item": map[string]any{
				"type": "message", "role": "user",
				"content": []map[string]string{{"type": "input_text", "text": body}},
			},
		},
		map[string]any{"type": "response.create"},
	}
	for _, ev := range events {
		raw, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("voice/live: encode: %w", err)
		}
		if err := l.ws.Write(ctx, websocket.MessageText, raw); err != nil {
			return l.fail(err)
		}
	}
	return nil
}

// liveEvent is the subset of a server event this client consumes.
type liveEvent struct {
	Type       string `json:"type"`
	Delta      string `json:"delta"`
	Transcript string `json:"transcript"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
	Response *struct {
		Status string `json:"status"`
	} `json:"response"`
}

// apply folds one event into the answer.
func (l *Live) apply(ev *liveEvent, audio, reply *strings.Builder) (bool, error) {
	switch ev.Type {
	case "response.audio.delta":
		chunk, err := base64.StdEncoding.DecodeString(ev.Delta)
		if err != nil {
			return false, fmt.Errorf("%w: audio delta", ErrLiveProtocol)
		}
		if int64(audio.Len()+len(chunk)) > l.max {
			return false, ErrLiveTooLarge
		}
		audio.Write(chunk)
	case "response.audio_transcript.delta", "response.text.delta":
		reply.WriteString(ev.Delta)
	case "response.audio_transcript.done", "response.text.done":
		if reply.Len() == 0 {
			reply.WriteString(ev.Transcript)
		}
	case "error":
		msg := "provider error"
		if ev.Error != nil && ev.Error.Message != "" {
			msg = clipAudit(ev.Error.Message)
		}
		return false, fmt.Errorf("%w: %s", ErrLive, msg)
	case "response.done":
		if ev.Response != nil && ev.Response.Status == "failed" {
			return false, fmt.Errorf("%w: response failed", ErrLive)
		}
		return true, nil
	}
	return false, nil
}

// Close closes the WebSocket with a normal closure.
func (l *Live) Close() error {
	if l == nil || l.ws == nil {
		return nil
	}
	err := l.ws.Close(websocket.StatusNormalClosure, "")
	l.ws = nil
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	return fmt.Errorf("voice/live: close: %w", err)
}

func (l *Live) fail(err error) error {
	if l.ws != nil {
		cerr := l.ws.CloseNow()
		l.ws = nil
		err = errors.Join(err, cerr)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: timed out", ErrLive)
	}
	return fmt.Errorf("%w: %w", ErrLive, err)
}

// realtimeURL upgrades an http(s) endpoint to ws(s) and injects the model.
func realtimeURL(endpoint, model string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Host == "" {
		return "", errors.New("endpoint must be an http(s) URL")
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", errors.New("endpoint must be an http(s) URL")
	}
	if model != "" {
		q := u.Query()
		if q.Get("model") == "" {
			q.Set("model", model)
			u.RawQuery = q.Encode()
		}
	}
	return u.String(), nil
}

// clipAudit keeps a provider error short; it never includes the key.
func clipAudit(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
