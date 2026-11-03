// Package stt is the speech-to-text half of the voice module (SPEC §9.5): the
// Provider contract, whisper.cpp as an executable, and bounded WAV chunking.
package stt

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// MaxAudioBytes is the largest accepted input (25 MiB).
	MaxAudioBytes = 25 << 20
	// DefaultChunkSeconds is the time boundary of one whisper call.
	DefaultChunkSeconds = 600
	// MaxChunks bounds total duration (12 × 600 s = 2 h).
	MaxChunks = 12
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
	errOutput           = errors.New("voice/stt: unreadable whisper output")
	errUnsupportedAudio = errors.New("voice/stt: audio format cannot be chunked")
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
	Timeout      time.Duration
	ChunkSeconds int
	MaxBytes     int64
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
	f, size, err := spool(dir, r, o.MaxBytes)
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
	plans, err := planChunks(f, size, tr.MIME, dir, o.ChunkSeconds)
	if err != nil {
		return Transcript{}, err
	}
	tr.Chunks = len(plans)
	for i, c := range plans {
		segs, rerr := w.run(ctx, dir, c.path, o, i)
		if rerr != nil {
			return Transcript{}, rerr
		}
		for _, s := range segs {
			s.Start += c.offset
			s.End += c.offset
			tr.Segments = append(tr.Segments, s)
		}
	}
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

type wavHeader struct {
	codec, channels, blockAlign, bits uint16
	sampleRate, byteRate              uint32
	dataLen                           int64
}

// readWAV reads a canonical 44-byte PCM header; ok is false for other layouts.
func readWAV(f *os.File, size int64) (wavHeader, bool) {
	var h wavHeader
	var b [44]byte
	if size < 44 {
		return h, false
	}
	if _, err := f.ReadAt(b[:], 0); err != nil {
		return h, false
	}
	if string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" || string(b[12:16]) != "fmt " || string(b[36:40]) != "data" {
		return h, false
	}
	h.codec, h.channels = binary.LittleEndian.Uint16(b[20:]), binary.LittleEndian.Uint16(b[22:])
	h.sampleRate, h.byteRate = binary.LittleEndian.Uint32(b[24:]), binary.LittleEndian.Uint32(b[28:])
	h.blockAlign, h.bits = binary.LittleEndian.Uint16(b[32:]), binary.LittleEndian.Uint16(b[34:])
	h.dataLen = min(int64(binary.LittleEndian.Uint32(b[40:])), size-44)
	return h, true
}

// chunk is one spill file and the recording offset of its first sample.
type chunk struct {
	path   string
	offset time.Duration
}

// planChunks returns one plan unless the input is long canonical WAV PCM.
func planChunks(f *os.File, size int64, mime, dir string, seconds int) ([]chunk, error) {
	if mime != "audio/wav" {
		return []chunk{{path: f.Name()}}, nil
	}
	h, ok := readWAV(f, size)
	if !ok || time.Duration(h.dataLen)*time.Second/time.Duration(h.byteRate) <= time.Duration(seconds)*time.Second {
		return []chunk{{path: f.Name()}}, nil
	}
	return splitWAV(f, h, dir, seconds)
}

// splitWAV writes at most MaxChunks valid WAV files on frame boundaries.
func splitWAV(f *os.File, h wavHeader, dir string, seconds int) ([]chunk, error) {
	if (h.codec != 1 && h.codec != 3) || h.blockAlign == 0 || h.sampleRate == 0 || seconds <= 0 {
		return nil, fmt.Errorf("%w: WAV codec 0x%04x cannot be split", errUnsupportedAudio, h.codec)
	}
	framesPer := int64(seconds) * int64(h.sampleRate)
	total := h.dataLen / int64(h.blockAlign)
	n := (total + framesPer - 1) / framesPer
	if n > MaxChunks {
		return nil, fmt.Errorf("%w: %d chunks > %d", ErrTooLarge, n, MaxChunks)
	}
	out := make([]chunk, 0, n)
	for i := range n {
		frames := min(framesPer, total-i*framesPer)
		path, err := writeChunk(f, h, dir, 44+i*framesPer*int64(h.blockAlign), frames*int64(h.blockAlign))
		if err != nil {
			return nil, err
		}
		out = append(out, chunk{path: path, offset: time.Duration(i*framesPer) * time.Second / time.Duration(h.sampleRate)})
	}
	return out, nil
}

// writeChunk copies the canonical header and the source frames.
func writeChunk(f *os.File, h wavHeader, dir string, start, length int64) (string, error) {
	if length < 0 || length > math.MaxUint32-36 {
		return "", fmt.Errorf("%w: chunk length %d", ErrTooLarge, length)
	}
	tf, err := os.CreateTemp(dir, "chunk-*.wav")
	if err != nil {
		return "", fmt.Errorf("voice/stt: chunk: %w", err)
	}
	b := make([]byte, 44)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+length))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], h.codec)
	binary.LittleEndian.PutUint16(b[22:], h.channels)
	binary.LittleEndian.PutUint32(b[24:], h.sampleRate)
	binary.LittleEndian.PutUint32(b[28:], h.byteRate)
	binary.LittleEndian.PutUint16(b[32:], h.blockAlign)
	binary.LittleEndian.PutUint16(b[34:], h.bits)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(length))
	_, werr := tf.Write(b)
	if werr == nil {
		_, werr = io.CopyN(tf, io.NewSectionReader(f, start, length), length)
	}
	if err := errors.Join(werr, tf.Close()); err != nil {
		return "", fmt.Errorf("voice/stt: chunk: %w", err)
	}
	return tf.Name(), nil
}

// withDefaults resolves the zero-value knobs once.
func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.ChunkSeconds <= 0 {
		o.ChunkSeconds = DefaultChunkSeconds
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
