package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Wire is the request shape of one STT provider.
type Wire string

// Provider wires.
const (
	WireWhisper Wire = "whispercpp"
	WireOpenAI  Wire = "openai"
	WireCommand Wire = "command"
)

// Spec is one provider's explicit defaults.
type Spec struct {
	ID    string
	Wire  Wire
	Model string
}

// providers is the closed STT catalog (MEDIA-010). Cloud providers use the
// OpenAI-compatible multipart endpoint when configured; every other
// provider is invoked through an explicit stt_command argv (no shell).
func providers() []Spec {
	return []Spec{
		{ID: "whispercpp", Wire: WireWhisper},
		{ID: "local_command", Wire: WireCommand},
		{ID: "groq", Wire: WireOpenAI, Model: "whisper-large-v3"},
		{ID: "openai", Wire: WireOpenAI, Model: "whisper-1"},
		{ID: "mistral", Wire: WireOpenAI, Model: "whisper-1"},
		{ID: "xai", Wire: WireCommand},
		{ID: "elevenlabs", Wire: WireCommand},
		{ID: "deepinfra", Wire: WireOpenAI, Model: "whisper-1"},
	}
}

// Catalog returns the ordered provider ids.
func Catalog() []string {
	specs := providers()
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.ID
	}
	return out
}

// SpecOf returns one provider's explicit defaults.
func SpecOf(id string) (Spec, bool) {
	for _, s := range providers() {
		if s.ID == id {
			return s, true
		}
	}
	return Spec{}, false
}

// Doer is the egress seam of the HTTP provider.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Factory builds the selected provider from explicit configuration.
type Factory struct {
	Endpoint, Key, Model     string
	WhisperBin, WhisperModel string
	Command                  []string
	Doer                     Doer
	MaxBytes                 int64
	Timeout                  time.Duration
}

// New returns the provider named by id and fails closed without its explicit
// endpoint, command, or whisper paths.
func (f *Factory) New(id string) (Provider, error) {
	spec, ok := SpecOf(id)
	if !ok {
		return nil, fmt.Errorf("voice/stt: unknown provider %q", id)
	}
	switch spec.Wire {
	case WireWhisper:
		if f == nil {
			return nil, fmt.Errorf("%w: whispercpp needs whisper_bin and whisper_model", errNotConfigured)
		}
		return NewWhisper(f.WhisperBin, f.WhisperModel)
	case WireOpenAI:
		if f == nil || strings.TrimSpace(f.Endpoint) == "" {
			return nil, fmt.Errorf("%w: %s needs stt_endpoint", errNotConfigured, id)
		}
		model := f.Model
		if model == "" {
			model = spec.Model
		}
		return NewHTTP(&HTTPConfig{Endpoint: f.Endpoint, Key: f.Key, Model: model, Backend: id, MaxBytes: f.MaxBytes, Timeout: f.Timeout, Doer: f.Doer})
	case WireCommand:
		if f == nil || len(f.Command) == 0 {
			return nil, fmt.Errorf("%w: %s needs stt_command (argv, no shell)", errNotConfigured, id)
		}
		return NewCommand(id, f.Command, f.MaxBytes, f.Timeout)
	default:
		return nil, fmt.Errorf("%w: %s has no wire", errNotConfigured, id)
	}
}

// HTTPConfig is one OpenAI-compatible transcription endpoint.
type HTTPConfig struct {
	Endpoint, Key, Model, Backend string
	MaxBytes                      int64
	Timeout                       time.Duration
	Doer                          Doer
}

// HTTP posts one multipart recording to /audio/transcriptions.
type HTTP struct {
	endpoint, key, model, backend string
	max                           int64
	timeout                       time.Duration
	doer                          Doer
}

// NewHTTP validates the endpoint and the egress seam.
func NewHTTP(c *HTTPConfig) (*HTTP, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: no config", errNotConfigured)
	}
	ep := strings.TrimSpace(c.Endpoint)
	if u, err := url.Parse(ep); ep == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%w: endpoint must be an http(s) URL", errNotConfigured)
	}
	if c.Doer == nil {
		return nil, fmt.Errorf("%w: no HTTP doer", errNotConfigured)
	}
	if c.MaxBytes <= 0 {
		c.MaxBytes = MaxAudioBytes
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	return &HTTP{endpoint: ep, key: c.Key, model: c.Model, backend: c.Backend, max: c.MaxBytes, timeout: c.Timeout, doer: c.Doer}, nil
}

// Transcribe uploads one recording and parses the JSON text field.
func (h *HTTP) Transcribe(ctx context.Context, r io.Reader, o Options) (Transcript, error) {
	body, contentType, mime, err := h.upload(r, o)
	if err != nil {
		return Transcript{}, err
	}
	text, err := h.post(ctx, body, contentType, o)
	if err != nil {
		return Transcript{}, err
	}
	return Transcript{Text: text, Backend: h.backend, MIME: mime, Chunks: 1}, nil
}

// upload reads one bounded recording and builds its multipart body.
func (h *HTTP) upload(r io.Reader, o Options) (*bytes.Buffer, string, string, error) {
	if r == nil {
		return nil, "", "", ErrEmptyAudio
	}
	max := o.MaxBytes
	if max <= 0 || max > h.max {
		max = h.max
	}
	raw, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, "", "", fmt.Errorf("voice/stt: read: %w", err)
	}
	switch {
	case len(raw) == 0:
		return nil, "", "", ErrEmptyAudio
	case int64(len(raw)) > max:
		return nil, "", "", fmt.Errorf("%w: over %d bytes", ErrTooLarge, max)
	}
	if needsFFmpeg(raw) {
		return nil, "", "", ErrContainerUnavailable
	}
	mime := mimeOf(raw)
	body, contentType, err := h.formBody(raw, mime)
	if err != nil {
		return nil, "", "", err
	}
	return body, contentType, mime, nil
}

// post sends the upload and returns the bounded JSON text field.
func (h *HTTP) post(ctx context.Context, body *bytes.Buffer, contentType string, o Options) (string, error) {
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = h.timeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, h.endpoint, body)
	if err != nil {
		return "", fmt.Errorf("voice/stt: request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	if h.key != "" {
		req.Header.Set("Authorization", "Bearer "+h.key)
	}
	resp, err := h.doer.Do(req)
	if err != nil {
		if cctx.Err() != nil {
			return "", fmt.Errorf("%w: %w", ErrTimeout, cctx.Err())
		}
		return "", fmt.Errorf("voice/stt: request: %w", err)
	}
	out, rerr := io.ReadAll(io.LimitReader(resp.Body, maxJSON+1))
	if jerr := errors.Join(rerr, resp.Body.Close()); jerr != nil {
		return "", fmt.Errorf("voice/stt: body: %w", jerr)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("%w: %s", errProcess, clipText(resp.Status))
	}
	if len(out) > maxJSON {
		return "", fmt.Errorf("%w: over %d bytes", errOutput, maxJSON)
	}
	var doc struct {
		Text string `json:"text"`
	}
	if jerr := json.Unmarshal(out, &doc); jerr != nil {
		return "", fmt.Errorf("%w: %w", errOutput, jerr)
	}
	text := strings.TrimSpace(doc.Text)
	if text == "" {
		return "", fmt.Errorf("%w: empty transcript", errOutput)
	}
	return text, nil
}

// formBody builds the multipart upload once.
func (h *HTTP) formBody(raw []byte, mime string) (*bytes.Buffer, string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	name := "audio"
	if mime != "" {
		if ext, ok := sttExt(mime); ok {
			name += ext
		}
	}
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		return nil, "", fmt.Errorf("voice/stt: form: %w", err)
	}
	if _, werr := part.Write(raw); werr != nil {
		return nil, "", fmt.Errorf("voice/stt: form: %w", werr)
	}
	if werr := mw.WriteField("model", h.model); werr != nil {
		return nil, "", fmt.Errorf("voice/stt: form: %w", werr)
	}
	if werr := mw.Close(); werr != nil {
		return nil, "", fmt.Errorf("voice/stt: form: %w", werr)
	}
	return &body, mw.FormDataContentType(), nil
}

// Command is one external transcription executable. The recording path is
// passed as one argv element; stdout is the transcript text.
type Command struct {
	backend string
	argv    []string
	max     int64
	timeout time.Duration
}

// MaxCommandArgs bounds one argv (config validation uses the same limit).
const MaxCommandArgs = 32

// NewCommand validates and resolves the argv; the executable must exist.
func NewCommand(backend string, argv []string, max int64, timeout time.Duration) (*Command, error) {
	if len(argv) == 0 || len(argv) > MaxCommandArgs {
		return nil, fmt.Errorf("%w: command argv must have 1..%d elements", errNotConfigured, MaxCommandArgs)
	}
	args := make([]string, len(argv))
	for i, arg := range argv {
		if strings.ContainsRune(arg, 0) || len(arg) > 4096 {
			return nil, fmt.Errorf("%w: command argument %d is invalid", errNotConfigured, i)
		}
		args[i] = arg
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		return nil, fmt.Errorf("%w: command %q: %w", errNotConfigured, args[0], err)
	}
	args[0] = path
	if max <= 0 {
		max = MaxAudioBytes
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Command{backend: backend, argv: args, max: max, timeout: timeout}, nil
}

// Transcribe spools the recording to a 0600 file and runs one bounded command.
func (c *Command) Transcribe(ctx context.Context, r io.Reader, o Options) (tr Transcript, err error) {
	if r == nil {
		return Transcript{}, ErrEmptyAudio
	}
	max := o.MaxBytes
	if max <= 0 || max > c.max {
		max = c.max
	}
	dir, err := os.MkdirTemp("", "damnbot-voice-stt-*")
	if err != nil {
		return Transcript{}, fmt.Errorf("voice/stt: temp dir: %w", err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	f, _, err := spool(dir, r, max)
	if err != nil {
		return Transcript{}, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	var head [16]byte
	n, rerr := f.ReadAt(head[:], 0)
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		return Transcript{}, fmt.Errorf("voice/stt: header: %w", rerr)
	}
	args := expandAudio(c.argv, f.Name())
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = c.timeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stdout limitedText
	stdout.max = maxJSON + 1
	cmd := exec.Cmd{Path: args[0], Args: args, Dir: dir, Stdout: &stdout, WaitDelay: time.Second}
	runErr := runCommand(cctx, &cmd)
	switch {
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		return Transcript{}, fmt.Errorf("%w after %s", ErrTimeout, timeout)
	case cctx.Err() != nil:
		return Transcript{}, fmt.Errorf("voice/stt: %w", cctx.Err())
	case runErr != nil:
		return Transcript{}, fmt.Errorf("%w: %w", errProcess, runErr)
	}
	text := strings.TrimSpace(stdout.String())
	if text == "" {
		return Transcript{}, fmt.Errorf("%w: empty transcript", errOutput)
	}
	if needsFFmpeg(head[:n]) {
		return Transcript{}, ErrContainerUnavailable
	}
	return Transcript{Text: text, Backend: c.backend, MIME: mimeOf(head[:n]), Chunks: 1}, nil
}

// expandAudio substitutes {audio}; a command without it gets the path appended.
func expandAudio(argv []string, audio string) []string {
	args := make([]string, len(argv))
	found := false
	for i, arg := range argv {
		if strings.Contains(arg, "{audio}") {
			found = true
		}
		args[i] = strings.ReplaceAll(arg, "{audio}", audio)
	}
	if !found {
		args = append(args, audio)
	}
	return args
}

// limitedText caps captured stdout so a broken command cannot exhaust memory.
type limitedText struct {
	bytes.Buffer
	max int64
}

func (b *limitedText) Write(p []byte) (int, error) {
	if int64(b.Len())+int64(len(p)) > b.max {
		return 0, ErrTooLarge
	}
	n, err := b.Buffer.Write(p)
	if err != nil {
		return n, fmt.Errorf("voice/stt: buffer: %w", err)
	}
	return n, nil
}

// sttExt returns the container extension of a recognised MIME type.
func sttExt(mt string) (string, bool) {
	switch mt {
	case "audio/wav":
		return ".wav", true
	case "audio/mpeg":
		return ".mp3", true
	case "audio/ogg":
		return ".ogg", true
	case "audio/flac":
		return ".flac", true
	}
	return "", false
}

// clipText keeps a provider status short and free of query strings.
func clipText(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
