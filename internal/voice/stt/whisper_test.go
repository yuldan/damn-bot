package stt

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

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
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o600); err != nil {
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
	tr, err := w.Transcribe(t.Context(), bytes.NewReader(wavBytes(t, 16000, 16000)), Options{})
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
	_, err = w.Transcribe(t.Context(), bytes.NewReader(wavBytes(t, 8000, 8000)), Options{Timeout: 100 * time.Millisecond})
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
	if _, err := w.Transcribe(t.Context(), bytes.NewReader(nil), Options{}); !errors.Is(err, ErrEmptyAudio) {
		t.Errorf("empty err = %v", err)
	}
	if _, err := w.Transcribe(t.Context(), bytes.NewReader(make([]byte, 100)), Options{MaxBytes: 10}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize err = %v", err)
	}
	if _, err := w.Transcribe(t.Context(), nil, Options{}); !errors.Is(err, ErrEmptyAudio) {
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
	tr, err := w.Transcribe(t.Context(), bytes.NewReader(mp3), Options{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("mp3: %v", err)
	}
	if tr.MIME != "audio/mpeg" || tr.Chunks != 1 {
		t.Errorf("mp3 transcript = %+v", tr)
	}
	// A non-canonical WAV is sent whole, never guessed at or rewritten.
	broken := append([]byte("RIFF\x00\x00\x00\x00WAVE"), make([]byte, 40)...)
	tr, err = w.Transcribe(t.Context(), bytes.NewReader(broken), Options{})
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
	if _, err := w.Transcribe(t.Context(), bytes.NewReader(wavBytes(t, 8000, 800)), Options{}); !errors.Is(err, errOutput) {
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
	if _, err := w.Transcribe(ctx, bytes.NewReader(wavBytes(t, 8000, 800)), Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
