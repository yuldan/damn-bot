package stt

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil"
)

// transcribe runs one whisper call and retries only ETXTBSY: the fixture
// script is written by the test process, so a fork from a parallel test can
// still hold its writer when whisper is exec'd. Transcribe spools (consumes)
// the reader before exec, so a retried attempt must rewind a seekable reader;
// otherwise the retry would see EOF and mask ETXTBSY as ErrEmptyAudio.
func transcribe(ctx context.Context, t *testing.T, w *Whisper, r io.Reader, o Options) (Transcript, error) {
	t.Helper()
	var tr Transcript
	err := testutil.ExecRetry(ctx, func() error {
		if s, ok := r.(io.Seeker); ok {
			if _, serr := s.Seek(0, io.SeekStart); serr != nil {
				return fmt.Errorf("rewind: %w", serr)
			}
		}
		var rerr error
		tr, rerr = w.Transcribe(ctx, r, o)
		return rerr
	})
	if err != nil {
		return tr, fmt.Errorf("transcribe: %w", err)
	}
	return tr, nil
}

// wavBytes builds a mono PCM16 WAV of samples frames at rate Hz.
func wavBytes(t *testing.T, rate, samples uint32) []byte {
	t.Helper()
	data := make([]byte, samples*2)
	var hdr [44]byte
	copy(hdr[0:4], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], 36+samples*2)
	copy(hdr[8:12], "WAVE")
	copy(hdr[12:16], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1)
	binary.LittleEndian.PutUint16(hdr[22:], 1)
	binary.LittleEndian.PutUint32(hdr[24:], rate)
	binary.LittleEndian.PutUint32(hdr[28:], rate*2)
	binary.LittleEndian.PutUint16(hdr[32:], 2)
	binary.LittleEndian.PutUint16(hdr[34:], 16)
	copy(hdr[36:40], "data")
	binary.LittleEndian.PutUint32(hdr[40:], samples*2)
	return append(hdr[:], data...)
}

// fakeWhisper writes an executable script that emits one JSON document per
// -of prefix. Scripts are 0600 then chmod 0700, so no test needs an unsafe
// WriteFile mode.
func fakeWhisper(t *testing.T, dir, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	path := filepath.Join(dir, "fake-whisper")
	if err := testutil.WriteExecutable(path, []byte("#!/bin/sh\n"+body), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	makeExec(t, path)
	return path
}

func fakeModel(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "ggml-model.bin")
	if err := os.WriteFile(path, []byte("model"), 0o600); err != nil {
		t.Fatalf("write model: %v", err)
	}
	return path
}

const oneSegment = `prefix=""
while [ $# -gt 0 ]; do
	case "$1" in
	-of) prefix=$2; shift 2 ;;
	*) shift ;;
	esac
done
printf '%s' '{"transcription":[{"offsets":{"from":0,"to":1200},"text":" hello world"}]}' > "$prefix.json"
`

func TestWhisperTranscribe(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := fakeWhisper(t, dir, oneSegment)
	w, err := NewWhisper(bin, fakeModel(t, dir))
	if err != nil {
		t.Fatalf("NewWhisper: %v", err)
	}
	tr, err := transcribe(t.Context(), t, w, bytes.NewReader(wavBytes(t, 16000, 16000)), Options{})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if tr.Text != "hello world" || tr.Backend != "whispercpp" || tr.MIME != "audio/wav" || tr.Chunks != 1 {
		t.Fatalf("transcript = %+v", tr)
	}
	if len(tr.Segments) != 1 {
		t.Fatalf("segments = %+v", tr.Segments)
	}
	if s := tr.Segments[0]; s.Start != 0 || s.End != 1200*time.Millisecond || s.Text != "hello world" {
		t.Errorf("segment = %+v", s)
	}
}

func TestWhisperTimeout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := fakeWhisper(t, dir, "exec sleep 5\n")
	w, err := NewWhisper(bin, fakeModel(t, dir))
	if err != nil {
		t.Fatalf("NewWhisper: %v", err)
	}
	start := time.Now()
	_, err = transcribe(t.Context(), t, w, bytes.NewReader(wavBytes(t, 8000, 8000)), Options{Timeout: 100 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("timeout took %s", elapsed)
	}
}

func TestWhisperMissingBinaryAndModel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := NewWhisper(filepath.Join(dir, "nope"), fakeModel(t, dir)); !errors.Is(err, errBinaryMissing) {
		t.Fatalf("missing binary err = %v", err)
	}
	if _, err := NewWhisper(fakeWhisper(t, dir, oneSegment), filepath.Join(dir, "nope.bin")); !errors.Is(err, errNotConfigured) {
		t.Fatalf("missing model err = %v", err)
	}
	if _, err := NewWhisper("", ""); !errors.Is(err, errNotConfigured) {
		t.Fatalf("empty config err = %v", err)
	}
}

func TestWhisperEmptyAndOversized(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w, err := NewWhisper(fakeWhisper(t, dir, oneSegment), fakeModel(t, dir))
	if err != nil {
		t.Fatalf("NewWhisper: %v", err)
	}
	if _, err := transcribe(t.Context(), t, w, bytes.NewReader(nil), Options{}); !errors.Is(err, ErrEmptyAudio) {
		t.Errorf("empty err = %v", err)
	}
	if _, err := transcribe(t.Context(), t, w, bytes.NewReader(make([]byte, 100)), Options{MaxBytes: 10}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize err = %v", err)
	}
	if _, err := transcribe(t.Context(), t, w, nil, Options{}); !errors.Is(err, ErrEmptyAudio) {
		t.Errorf("nil reader err = %v", err)
	}
}

func TestWhisperNonWAVAndMalformedWAV(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w, err := NewWhisper(fakeWhisper(t, dir, oneSegment), fakeModel(t, dir))
	if err != nil {
		t.Fatalf("NewWhisper: %v", err)
	}
	mp3 := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 64)...)
	tr, err := transcribe(t.Context(), t, w, bytes.NewReader(mp3), Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("mp3: %v", err)
	}
	if tr.MIME != "audio/mpeg" || tr.Chunks != 1 {
		t.Errorf("mp3 transcript = %+v", tr)
	}
	// A non-canonical WAV is sent whole, never guessed at or rewritten.
	broken := append([]byte("RIFF\x00\x00\x00\x00WAVE"), make([]byte, 40)...)
	tr, err = transcribe(t.Context(), t, w, bytes.NewReader(broken), Options{})
	if err != nil || tr.Chunks != 1 || tr.MIME != "audio/wav" {
		t.Errorf("non-canonical wav = %+v, err = %v", tr, err)
	}
}

func TestWhisperBadOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := fakeWhisper(t, dir, `prefix=""
while [ $# -gt 0 ]; do case "$1" in -of) prefix=$2; shift 2 ;; *) shift ;; esac; done
printf '%s' 'not json' > "$prefix.json"
`)
	w, err := NewWhisper(bin, fakeModel(t, dir))
	if err != nil {
		t.Fatalf("NewWhisper: %v", err)
	}
	if _, err := transcribe(t.Context(), t, w, bytes.NewReader(wavBytes(t, 8000, 800)), Options{}); !errors.Is(err, errOutput) {
		t.Fatalf("err = %v, want errOutput", err)
	}
}

func TestTranscribeHonoursCancel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w, err := NewWhisper(fakeWhisper(t, dir, oneSegment), fakeModel(t, dir))
	if err != nil {
		t.Fatalf("NewWhisper: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := transcribe(ctx, t, w, bytes.NewReader(wavBytes(t, 8000, 800)), Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestF6MEDIA011WhisperChunking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := fakeWhisper(t, dir, `prefix=""
while [ $# -gt 0 ]; do
	case "$1" in
	-of) prefix=$2; shift 2 ;;
	*) shift ;;
	esac
done
printf '%s' '{"transcription":[{"offsets":{"from":0,"to":1200},"text":" hello world"}]}' > "$prefix.json"
`)
	w, err := NewWhisper(bin, fakeModel(t, dir))
	if err != nil {
		t.Fatalf("NewWhisper: %v", err)
	}
	// 5 s at 8 kHz, 2 s chunks -> 3 calls at 0, 2 s, 4 s.
	tr, err := transcribe(t.Context(), t, w, bytes.NewReader(wavBytes(t, 8000, 40000)), Options{ChunkSeconds: 2})
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if tr.Chunks != 3 {
		t.Fatalf("chunks = %d, want 3", tr.Chunks)
	}
	want := []time.Duration{0, 2 * time.Second, 4 * time.Second}
	if len(tr.Segments) != len(want) {
		t.Fatalf("segments = %+v", tr.Segments)
	}
	for i, s := range tr.Segments {
		if s.Start != want[i] {
			t.Errorf("segment %d start = %s, want %s", i, s.Start, want[i])
		}
	}
	if tr.Text != "hello world hello world hello world" {
		t.Errorf("text = %q", tr.Text)
	}
	// 80 MiB / 60 min at 11 kHz mono is split by time boundary without
	// ffmpeg: 6 chunks of 600 s.
	big := wavBytes(t, 11025, 11025*3600)
	if want := int64(96 << 20); int64(len(big)) > want {
		t.Fatalf("fixture %d > cap %d", len(big), want)
	}
	tr, err = w.Transcribe(t.Context(), bytes.NewReader(big), Options{})
	if err != nil {
		t.Fatalf("80 MiB Transcribe: %v", err)
	}
	if tr.Chunks != 6 || tr.MIME != "audio/wav" {
		t.Fatalf("80 MiB chunks = %d mime = %q", tr.Chunks, tr.MIME)
	}
	// M4A/MP4 needs ffmpeg: fail closed with module_unavailable.
	m4a := append([]byte("\x00\x00\x00\x18ftypM4A "), make([]byte, 64)...)
	if _, err := w.Transcribe(t.Context(), bytes.NewReader(m4a), Options{}); !errors.Is(err, ErrContainerUnavailable) {
		t.Fatalf("m4a err = %v, want ErrContainerUnavailable", err)
	}
}

func TestWhisperTooManyChunks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w, err := NewWhisper(fakeWhisper(t, dir, oneSegment), fakeModel(t, dir))
	if err != nil {
		t.Fatalf("NewWhisper: %v", err)
	}
	// 13 s at 8 kHz with 1 s chunks = 13 > MaxChunks.
	_, err = transcribe(t.Context(), t, w, bytes.NewReader(wavBytes(t, 8000, 13*8000)), Options{ChunkSeconds: 1})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestSplitWAVWritesPrivateChunks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	raw := wavBytes(t, 8000, 24000) // 3 s
	src := filepath.Join(dir, "src.wav")
	if err := os.WriteFile(src, raw, 0o600); err != nil {
		t.Fatalf("write wav: %v", err)
	}
	f := openIn(t, dir, "src.wav")
	info, ok := readWAV(f, int64(len(raw)))
	if !ok {
		t.Fatal("readWAV: not canonical")
	}
	chunks, err := splitWAV(f, info, dir, 1)
	if err != nil {
		t.Fatalf("splitWAV: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3", len(chunks))
	}
	for i, c := range chunks {
		st, err := os.Stat(c.path)
		if err != nil {
			t.Fatalf("stat chunk: %v", err)
		}
		// Windows stores only the read-only bit; Stat reports 0666.
		if runtime.GOOS != "windows" {
			if perm := st.Mode().Perm(); perm != 0o600 {
				t.Errorf("chunk %d mode = %o, want 600", i, perm)
			}
		}
		if want := time.Duration(i) * time.Second; c.offset != want {
			t.Errorf("chunk %d offset = %s, want %s", i, c.offset, want)
		}
	}
}

// openIn opens name under dir without a user-controlled os.Open path.
func openIn(t *testing.T, dir, name string) *os.File {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	t.Cleanup(func() {
		if cerr := root.Close(); cerr != nil {
			t.Errorf("close root: %v", cerr)
		}
	})
	f, err := root.Open(name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("close %s: %v", name, cerr)
		}
	})
	return f
}

func TestReadWAVRejectsNonCanonical(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	raw := append([]byte("RIFF\x10\x00\x00\x00WAVEfmt "), make([]byte, 40)...)
	path := filepath.Join(dir, "short.wav")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := readWAV(openIn(t, dir, "short.wav"), int64(len(raw))); ok {
		t.Fatal("readWAV accepted a non-canonical header")
	}
}

func TestSplitWAVRejectsCompressedCodec(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	raw := wavBytes(t, 8000, 16000)
	binary.LittleEndian.PutUint16(raw[20:], 2) // ADPCM, not PCM/float
	path := filepath.Join(dir, "adpcm.wav")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	f := openIn(t, dir, "adpcm.wav")
	info, ok := readWAV(f, int64(len(raw)))
	if !ok {
		t.Fatal("readWAV rejected a canonical header")
	}
	if _, err := splitWAV(f, info, dir, 1); !errors.Is(err, errUnsupportedAudio) {
		t.Fatalf("err = %v, want errUnsupportedAudio", err)
	}
}
