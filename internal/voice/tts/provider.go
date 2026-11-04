package tts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrTimeout is a provider call that outlived its budget.
var ErrTimeout = errors.New("voice/tts: provider timed out")

// Wire is the request shape of one provider. Every catalogued provider has
// exactly one explicit path: an OpenAI-compatible endpoint or an external
// command argv. Nothing is autodetected and no shell interprets an argument.
type Wire string

// Provider wires.
const (
	WireOpenAI  Wire = "openai"
	WireCommand Wire = "command"
)

// Spec is one provider's explicit defaults.
type Spec struct {
	ID    string
	Wire  Wire
	Model string
	Voice string
}

// providers is the closed TTS catalog (MEDIA-007 / TOOL-080). The OpenAI
// family speaks the OpenAI-compatible speech shape; every other provider is
// invoked through an explicit [modules.voice] tts_command argv.
func providers() []Spec {
	return []Spec{
		{ID: "edge", Wire: WireCommand},
		{ID: "elevenlabs", Wire: WireCommand},
		{ID: "openai", Wire: WireOpenAI, Model: "tts-1", Voice: "alloy"},
		{ID: "minimax", Wire: WireCommand},
		{ID: "xai", Wire: WireCommand},
		{ID: "mistral", Wire: WireCommand},
		{ID: "gemini", Wire: WireCommand},
		{ID: "neutts", Wire: WireCommand},
		{ID: "kittentts", Wire: WireCommand},
		{ID: "piper", Wire: WireCommand},
		{ID: "deepinfra", Wire: WireOpenAI},
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

// Factory builds the selected provider from explicit configuration.
type Factory struct {
	Endpoint, Key, Model, Voice, Format string
	Command                             []string
	Doer                                Doer
	MaxBytes                            int64
	Timeout                             time.Duration
}

// New returns the provider named by id. A provider whose wire needs an
// endpoint or a command that is not configured fails closed with
// [ErrNotConfigured]; there is no silent fallback to another provider.
func (f *Factory) New(id string) (Provider, error) {
	spec, ok := SpecOf(id)
	if !ok {
		return nil, fmt.Errorf("voice/tts: unknown provider %q", id)
	}
	switch spec.Wire {
	case WireOpenAI:
		if f == nil || strings.TrimSpace(f.Endpoint) == "" {
			return nil, fmt.Errorf("%w: %s needs tts_endpoint", ErrNotConfigured, id)
		}
		return NewHTTP(&Config{
			Endpoint: f.Endpoint, Key: f.Key, Backend: id,
			Model: cmp(f.Model, spec.Model), Voice: cmp(f.Voice, spec.Voice),
			Format: cmp(f.Format, "opus"), MaxBytes: f.MaxBytes, Doer: f.Doer,
		})
	case WireCommand:
		if f == nil || len(f.Command) == 0 {
			return nil, fmt.Errorf("%w: %s needs tts_command (argv, no shell)", ErrNotConfigured, id)
		}
		return NewCommand(id, f.Command, f.MaxBytes, f.Timeout)
	default:
		return nil, fmt.Errorf("%w: %s has no wire", ErrNotConfigured, id)
	}
}

// Command is one external TTS executable. Text is passed as one argv element,
// never through a shell; {out} may name the output file, otherwise stdout is
// the audio.
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
		return nil, fmt.Errorf("%w: command argv must have 1..%d elements", ErrNotConfigured, MaxCommandArgs)
	}
	args := make([]string, len(argv))
	for i, arg := range argv {
		if strings.ContainsRune(arg, 0) || len(arg) > 4096 {
			return nil, fmt.Errorf("%w: command argument %d is invalid", ErrNotConfigured, i)
		}
		args[i] = arg
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		return nil, fmt.Errorf("%w: command %q: %w", ErrNotConfigured, args[0], err)
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

// Synthesize runs one bounded command invocation.
func (c *Command) Synthesize(ctx context.Context, text string, o Options) (out Audio, err error) {
	body := strings.TrimSpace(text)
	if body == "" {
		return Audio{}, ErrEmptyText
	}
	if len(body) > MaxTextBytes {
		return Audio{}, fmt.Errorf("%w: %d > %d bytes", ErrTextTooLong, len(body), MaxTextBytes)
	}
	dir, err := os.MkdirTemp("", "damnbot-tts-*")
	if err != nil {
		return Audio{}, fmt.Errorf("voice/tts: temp dir: %w", err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	outPath := filepath.Join(dir, "out.audio")
	args, fileOut := expandArgs(c.argv, body, outPath)
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = c.timeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stdout limitedBuffer
	stdout.max = c.max + 1
	cmd := exec.Cmd{Path: args[0], Args: args, Dir: dir, WaitDelay: time.Second}
	if !fileOut {
		cmd.Stdout = &stdout
	}
	err = runCommand(cctx, &cmd)
	switch {
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		return Audio{}, fmt.Errorf("%w after %s", ErrTimeout, timeout)
	case cctx.Err() != nil:
		return Audio{}, fmt.Errorf("voice/tts: %w", cctx.Err())
	case err != nil:
		return Audio{}, fmt.Errorf("voice/tts: command %s failed: %w", c.backend, err)
	}
	var raw []byte
	if fileOut {
		raw, err = readLimited(dir, filepath.Base(outPath), c.max)
		if err != nil {
			return Audio{}, err
		}
	} else {
		raw = stdout.Bytes()
	}
	if int64(len(raw)) > c.max {
		return Audio{}, fmt.Errorf("%w: over %d bytes", ErrTooLarge, c.max)
	}
	a, err := classify(raw, "")
	if err != nil {
		return Audio{}, err
	}
	a.Backend = c.backend
	return a, nil
}

// expandArgs substitutes {text} and {out}; text is never split.
func expandArgs(argv []string, text, out string) ([]string, bool) {
	args := make([]string, len(argv))
	fileOut, hasText := false, false
	for i, arg := range argv {
		if strings.Contains(arg, "{out}") {
			fileOut = true
		}
		if strings.Contains(arg, "{text}") {
			hasText = true
		}
		arg = strings.ReplaceAll(arg, "{text}", text)
		args[i] = strings.ReplaceAll(arg, "{out}", out)
	}
	if !hasText {
		args = append(args, text)
	}
	return args, fileOut
}

// readLimited reads a command output file, refusing anything over max.
func readLimited(dir, name string, max int64) (raw []byte, err error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("voice/tts: command output: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	f, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("voice/tts: command output: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	raw, err = io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("voice/tts: command output: %w", err)
	}
	return raw, nil
}

// limitedBuffer caps captured stdout so a broken command cannot exhaust memory.
type limitedBuffer struct {
	bytes.Buffer
	max int64
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if int64(b.Len())+int64(len(p)) > b.max {
		return 0, ErrTooLarge
	}
	n, werr := b.Buffer.Write(p)
	if werr != nil {
		return n, fmt.Errorf("voice/tts: buffer: %w", werr)
	}
	return n, nil
}

// runCommand starts cmd and waits for it, killing it when ctx ends.
func runCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("voice/tts: start: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case werr := <-done:
		if werr != nil {
			return fmt.Errorf("voice/tts: wait: %w", werr)
		}
		return nil
	case <-ctx.Done():
		kerr := cmd.Process.Kill()
		werr := <-done
		if kerr != nil {
			kerr = fmt.Errorf("voice/tts: kill: %w", kerr)
		}
		if werr != nil {
			werr = fmt.Errorf("voice/tts: wait: %w", werr)
		}
		return fmt.Errorf("voice/tts: %w", errors.Join(ctx.Err(), kerr, werr))
	}
}
