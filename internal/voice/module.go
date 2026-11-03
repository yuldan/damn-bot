//go:build !minimal

package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/voice/stt"
	"github.com/damnbot/damnbot/internal/voice/tts"
)

var errBadConfig = errors.New("voice: invalid configuration")

// Section is [modules.voice] plus core network flags (ADR-319).
type Section struct {
	Enabled      bool   `json:"enabled"`
	STTBackend   string `json:"stt_backend"`
	TTSBackend   string `json:"tts_backend"`
	WhisperBin   string `json:"whisper_bin"`
	WhisperModel string `json:"whisper_model"`
	TTSEndpoint  string `json:"tts_endpoint"`
	TTSVoice     string `json:"tts_voice"`
	TTSKeyEnv    string `json:"tts_key_env"`
	Offline      bool   `json:"offline"`
	NoBackground bool   `json:"no_background"`
}

// Deps is the core-owned seam of the voice module.
type Deps struct {
	Approvals host.Approvals
	Secrets   host.Secrets
}

// Module is the F6 voice module.
type Module struct {
	cfg    Section
	deps   Deps
	tool   tts.Doer
	loop   tts.Doer
	mu     sync.Mutex
	status host.Status
}

// NewFromConfig decodes one strict section and validates its wiring.
func NewFromConfig(raw json.RawMessage, deps Deps) (*Module, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("%w: empty [modules.voice] section", errBadConfig)
	}
	var s Section
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%w: %w", errBadConfig, err)
	}
	return New(&s, deps)
}

// New validates the explicit provider selection; nothing starts or dials.
func New(s *Section, deps Deps) (*Module, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil config", errBadConfig)
	}
	if s.STTBackend != "" && !contains(Catalog(KindSTT), s.STTBackend) {
		return nil, fmt.Errorf("%w: stt backend %q", errBadConfig, s.STTBackend)
	}
	if s.TTSBackend != "" && !contains(Catalog(KindTTS), s.TTSBackend) {
		return nil, fmt.Errorf("%w: tts backend %q", errBadConfig, s.TTSBackend)
	}
	if deps.Approvals == nil {
		deps.Approvals = host.DenyApprovals{}
	}
	if deps.Secrets == nil {
		deps.Secrets = host.DenySecrets{}
	}
	network := netx.New(netx.Config{Offline: s.Offline, NoBackground: s.NoBackground})
	return &Module{
		cfg: *s, deps: deps, status: host.StatusStopped,
		tool: network.Client("voice", netx.Tool), loop: netx.LoopbackClient(),
	}, nil
}

func contains(list []string, want string) bool { return slices.Contains(list, want) }

// Start publishes the configured providers; the binary is checked on use.
func (m *Module) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("voice: start: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = host.StatusStopped
	if m.cfg.Enabled {
		m.status = host.StatusReady
	}
	return nil
}

// Status reports the lifecycle state.
func (m *Module) Status() host.Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Stop is idempotent; the module owns no process or goroutine.
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = host.StatusStopped
	return nil
}

// Invoke dispatches one voice tool; speak always asks with a clipped preview.
func (m *Module) Invoke(ctx context.Context, tool string, args json.RawMessage, env host.Env) (host.Result, error) {
	if m == nil || !m.cfg.Enabled {
		return host.Result{}, unavailable(tool)
	}
	switch tool {
	case ToolTranscribe:
		return m.transcribe(ctx, args, env)
	case ToolSpeak:
		return m.speak(ctx, args, env)
	}
	return host.Result{}, &host.Error{Code: "unknown_tool", Message: "unknown voice tool: " + tool}
}

func (m *Module) transcribe(ctx context.Context, args json.RawMessage, env host.Env) (out host.Result, err error) {
	var a struct {
		Path string `json:"path"`
	}
	if jerr := json.Unmarshal(args, &a); jerr != nil {
		return host.Result{}, &host.Error{Code: "invalid_args", Message: jerr.Error()}
	}
	provider, err := m.sttProvider()
	if err != nil {
		return host.Result{}, unavailability(ToolTranscribe)
	}
	cwd := env.Cwd
	if cwd == "" {
		cwd = env.Session.Cwd()
	}
	root, err := os.OpenRoot(cwd)
	if err != nil {
		return host.Result{}, &host.Error{Code: "invalid_args", Message: "workspace is not accessible"}
	}
	defer func() {
		if cerr := root.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("voice: close workspace: %w", cerr)
		}
	}()
	f, err := root.Open(a.Path)
	if err != nil {
		return host.Result{}, &host.Error{Code: "not_found", Message: "audio file is not readable"}
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("voice: close audio: %w", cerr)
		}
	}()
	tr, err := provider.Transcribe(ctx, f, stt.Options{MaxBytes: stt.MaxAudioBytes})
	if err != nil {
		return host.Result{}, voiceError(err)
	}
	return host.Result{
		Content:         tr.Text,
		Meta:            map[string]any{"backend": tr.Backend, "mime": tr.MIME, "chunks": tr.Chunks},
		UntrustedSource: "voice:transcribe",
	}, nil
}

func (m *Module) speak(ctx context.Context, args json.RawMessage, env host.Env) (host.Result, error) {
	var a struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return host.Result{}, &host.Error{Code: "invalid_args", Message: err.Error()}
	}
	preview := "speak " + clip(strings.TrimSpace(a.Text), 120)
	if err := m.approve(ctx, preview, env, args); err != nil {
		return host.Result{}, err
	}
	provider, err := m.ttsProvider()
	if err != nil {
		return host.Result{}, unavailability(ToolSpeak)
	}
	audio, err := provider.Synthesize(ctx, a.Text, tts.Options{})
	if err != nil {
		return host.Result{}, voiceError(err)
	}
	return host.Result{
		Content:         fmt.Sprintf("synthesized %s audio (%d bytes)", audio.MIME, len(audio.Bytes)),
		Meta:            map[string]any{"backend": audio.Backend, "mime": audio.MIME, "bytes": len(audio.Bytes)},
		UntrustedSource: "voice:speak",
	}, nil
}

func (m *Module) sttProvider() (STT, error) {
	cfg := &Config{STTBackend: m.cfg.STTBackend, WhisperBin: m.cfg.WhisperBin, WhisperModel: m.cfg.WhisperModel}
	return cfg.NewSTT()
}

func (m *Module) ttsProvider() (TTS, error) {
	key, err := m.secret(m.cfg.TTSKeyEnv)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		TTSBackend: m.cfg.TTSBackend, TTSEndpoint: m.cfg.TTSEndpoint,
		TTSVoice: m.cfg.TTSVoice, TTSKey: key, Doer: m.doer(m.cfg.TTSEndpoint),
	}
	return cfg.NewTTS()
}

func (m *Module) doer(endpoint string) tts.Doer {
	if loopbackURL(endpoint) {
		return m.loop
	}
	return m.tool
}

func (m *Module) secret(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	pairs, err := m.deps.Secrets.ChildEnv(host.AllowPolicy{Inherit: []string{name}})
	if err != nil {
		return "", fmt.Errorf("voice: secret: %w", err)
	}
	for _, pair := range pairs {
		if k, v, ok := strings.Cut(pair, "="); ok && k == name {
			return v, nil
		}
	}
	return "", nil
}

// approve raises one allow-once request with a clipped text preview.
func (m *Module) approve(ctx context.Context, preview string, env host.Env, args json.RawMessage) error {
	req := &host.ApprovalRequest{
		Tool: ToolSpeak, CallID: env.CallID, Effect: host.EffectNonIdempotent,
		Class: "voice:speak", Preview: preview, Args: args,
	}
	if err := req.Validate(); err != nil {
		return &host.Error{Code: "denied", Message: err.Error()}
	}
	answer, err := m.deps.Approvals.Approve(ctx, req)
	if err != nil {
		return &host.Error{Code: "denied", Message: err.Error()}
	}
	if answer != host.ApprovalAllowOnce {
		return &host.Error{Code: "denied", Message: "approval denied for speak"}
	}
	return nil
}

func clip(text string, max int) string {
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max]) + "…"
}

func unavailable(tool string) error {
	return &host.Error{Code: "module_unavailable", Message: "voice module is unavailable: " + tool}
}

func unavailability(tool string) error {
	return &host.Error{Code: "module_unavailable", Message: "voice backend is unavailable: " + tool}
}

func voiceError(err error) error {
	switch {
	case errors.Is(err, ErrModuleUnavailable), errors.Is(err, ErrUnknownProvider):
		return &host.Error{Code: "module_unavailable", Message: err.Error()}
	case errors.Is(err, stt.ErrTooLarge), errors.Is(err, tts.ErrTooLarge):
		return &host.Error{Code: "too_large", Message: err.Error()}
	case errors.Is(err, stt.ErrEmptyAudio), errors.Is(err, tts.ErrEmptyText), errors.Is(err, tts.ErrTextTooLong):
		return &host.Error{Code: "invalid_args", Message: err.Error()}
	case errors.Is(err, stt.ErrTimeout):
		return &host.Error{Code: "timeout", Message: err.Error()}
	default:
		return &host.Error{Code: "network", Message: err.Error()}
	}
}

func loopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	name := strings.ToLower(u.Hostname())
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(name)
	return err == nil && ip.Unmap().IsLoopback()
}
