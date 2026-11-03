// Package stt is the speech-to-text half of the voice module (SPEC §9.5): the
// Provider contract, whisper.cpp as an executable, and bounded WAV chunking.
package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// MaxAudioBytes is the largest accepted input (25 MiB).
	MaxAudioBytes = 25 << 20
	// DefaultTimeout bounds one whisper invocation.
	DefaultTimeout = 5 * time.Minute
	maxJSON        = 8 << 20
)

var (
	// errNotConfigured is a provider without an executable or model path.
	errNotConfigured = errors.New("voice/stt: provider is not configured")
	// errBinaryMissing is a whisper executable that does not exist.
	errBinaryMissing = errors.New("voice/stt: whisper binary not found")
	// ErrEmptyAudio is a zero-byte recording.
	ErrEmptyAudio = errors.New("voice/stt: empty audio")
	// ErrTooLarge is an input over MaxAudioBytes or MaxChunks.
	ErrTooLarge = errors.New("voice/stt: audio exceeds the limit")
	// ErrTimeout is a whisper call that outlived its budget.
	ErrTimeout = errors.New("voice/stt: transcription timed out")
	// errProcess is a whisper process that exited non-zero.
	errProcess = errors.New("voice/stt: whisper failed")
	// errOutput is a missing or unreadable whisper JSON document.
	errOutput = errors.New("voice/stt: unreadable whisper output")
)

// Segment is one timestamped span of a transcript, relative to the recording.
type Segment struct {
	Start time.Duration
	End   time.Duration
	Text  string
}

// Transcript is one recognised recording with an honest MIME type.
type Transcript struct {
	Text     string
	Segments []Segment
	Backend  string
	MIME     string
	Chunks   int
}

// Options are the per-call knobs of a transcription.
type Options struct {
	Timeout  time.Duration
	MaxBytes int64
}

// Provider is one speech-to-text backend from the closed catalog.
type Provider interface {
	Transcribe(ctx context.Context, audio io.Reader, opts Options) (Transcript, error)
}

func mimeOf(head []byte) string {
	switch {
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WAVE":
		return "audio/wav"
	case bytes.HasPrefix(head, []byte("OggS")):
		return "audio/ogg"
	case bytes.HasPrefix(head, []byte("fLaC")):
		return "audio/flac"
	case bytes.HasPrefix(head, []byte("ID3")):
		return "audio/mpeg"
	case len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0:
		return "audio/mpeg"
	}
	return ""
}

// Whisper is the whisper.cpp CLI backend; the executable and model come from
// [modules.voice] config and nothing is downloaded.
type Whisper struct {
	bin   string
	model string
}

// NewWhisper validates the executable and model path eagerly.
func NewWhisper(bin, model string) (*Whisper, error) {
	if strings.TrimSpace(bin) == "" || strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("%w: whisper_bin and whisper_model are required", errNotConfigured)
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errBinaryMissing, bin, err)
	}
	if _, err := os.Stat(model); err != nil {
		return nil, fmt.Errorf("%w: model %s: %w", errNotConfigured, model, err)
	}
	return &Whisper{bin: path, model: model}, nil
}

// Transcribe spools the input to a 0600 file, splits WAV PCM on time
// boundaries, and runs one bounded whisper process per chunk.
func (w *Whisper) Transcribe(ctx context.Context, r io.Reader, o Options) (tr Transcript, err error) {
	if r == nil {
		return Transcript{}, ErrEmptyAudio
	}
	if cerr := ctx.Err(); cerr != nil {
		return Transcript{}, fmt.Errorf("voice/stt: %w", cerr)
	}
	o = o.withDefaults()
	dir, err := os.MkdirTemp("", "damnbot-voice-stt-*")
	if err != nil {
		return Transcript{}, fmt.Errorf("voice/stt: temp dir: %w", err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	f, _, err := spool(dir, r, o.MaxBytes)
	if err != nil {
		return Transcript{}, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()

	var head [16]byte
	n, rerr := f.ReadAt(head[:], 0)
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		return Transcript{}, fmt.Errorf("voice/stt: header: %w", rerr)
	}
	tr = Transcript{Backend: "whispercpp", MIME: mimeOf(head[:n]), Chunks: 1}
	if tr.MIME == "" {
		tr.MIME = "application/octet-stream"
	}
	tr.Chunks = 1
	segs, rerr := w.run(ctx, dir, f.Name(), o, 0)
	if rerr != nil {
		return Transcript{}, rerr
	}
	tr.Segments = segs
	var b strings.Builder
	for _, s := range tr.Segments {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s.Text)
	}
	tr.Text = b.String()
	return tr, nil
}

func spool(dir string, r io.Reader, max int64) (*os.File, int64, error) {
	f, err := os.CreateTemp(dir, "audio-*")
	if err != nil {
		return nil, 0, fmt.Errorf("voice/stt: spool: %w", err)
	}
	n, cerr := io.Copy(f, io.LimitReader(r, max+1))
	if cerr = errors.Join(cerr, f.Close()); cerr != nil {
		return nil, 0, fmt.Errorf("voice/stt: spool: %w", cerr)
	}
	switch {
	case n == 0:
		return nil, 0, ErrEmptyAudio
	case n > max:
		return nil, 0, fmt.Errorf("%w: %d > %d bytes", ErrTooLarge, n, max)
	}
	f, err = os.Open(f.Name())
	if err != nil {
		return nil, 0, fmt.Errorf("voice/stt: spool: %w", err)
	}
	return f, n, nil
}

// withDefaults resolves the zero-value knobs once.
func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = MaxAudioBytes
	}
	return o
}

// runCommand starts cmd and waits for it, killing it when ctx ends.
func runCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("voice/stt: start: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case werr := <-done:
		return werr
	case <-ctx.Done():
		kerr := cmd.Process.Kill()
		werr := <-done
		if kerr != nil && !errors.Is(kerr, os.ErrProcessDone) {
			return fmt.Errorf("voice/stt: kill: %w", kerr)
		}
		if werr != nil && !errors.Is(werr, os.ErrProcessDone) {
			return fmt.Errorf("voice/stt: %w", werr)
		}
		return fmt.Errorf("voice/stt: %w", ctx.Err())
	}
}

func (w *Whisper) run(ctx context.Context, dir, audio string, o Options, index int) (segs []Segment, err error) {
	prefix := filepath.Join(dir, fmt.Sprintf("out-%02d", index))
	cctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	cmd := exec.Cmd{
		Path: w.bin,
		Args: append([]string{w.bin}, "-m", w.model, "-f", audio, "-oj", "-of", prefix, "-nt"),
		Dir:  dir, WaitDelay: time.Second,
	}
	if rerr := runCommand(cctx, &cmd); rerr != nil {
		switch {
		case errors.Is(cctx.Err(), context.DeadlineExceeded):
			return nil, fmt.Errorf("%w after %s", ErrTimeout, o.Timeout)
		case cctx.Err() != nil:
			return nil, fmt.Errorf("voice/stt: %w", cctx.Err())
		case errors.Is(rerr, exec.ErrNotFound):
			return nil, fmt.Errorf("%w: %w", errBinaryMissing, rerr)
		default:
			return nil, fmt.Errorf("%w: %w", errProcess, rerr)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("voice/stt: output root: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	out, err := root.Open(filepath.Base(prefix) + ".json")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errOutput, err)
	}
	raw, rerr := io.ReadAll(io.LimitReader(out, maxJSON+1))
	if err := errors.Join(rerr, out.Close()); err != nil {
		return nil, fmt.Errorf("%w: %w", errOutput, err)
	}
	if len(raw) > maxJSON {
		return nil, fmt.Errorf("%w: over %d bytes", errOutput, maxJSON)
	}
	var doc struct {
		Transcription []struct {
			Offsets struct {
				From int64 `json:"from"`
				To   int64 `json:"to"`
			} `json:"offsets"`
			Text string `json:"text"`
		} `json:"transcription"`
	}
	if jerr := json.Unmarshal(raw, &doc); jerr != nil {
		return nil, fmt.Errorf("%w: %w", errOutput, jerr)
	}
	segs = make([]Segment, 0, len(doc.Transcription))
	for _, s := range doc.Transcription {
		if text := strings.TrimSpace(s.Text); text != "" {
			segs = append(segs, Segment{time.Duration(s.Offsets.From) * time.Millisecond, time.Duration(s.Offsets.To) * time.Millisecond, text})
		}
	}
	return segs, nil
}
