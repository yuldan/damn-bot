package stt

import (
	"bytes"
	"errors"
	"testing"
)

// TestF6AudioMIME: honest MIME detection, internal 80 MB MP3 chunking without
// ffmpeg, and M4A failing closed when no external decoder exists.
func TestF6AudioMIME(t *testing.T) {
	cases := []struct {
		name string
		head []byte
		want string
	}{
		{"wav", append([]byte("RIFF\x00\x00\x00\x00WAVE"), make([]byte, 16)...), "audio/wav"},
		{"mp3-id3", append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 16)...), "audio/mpeg"},
		{"mp3-frame", []byte{0xFF, 0xFB, 0x90, 0x64, 0, 0, 0, 0, 0, 0, 0, 0}, "audio/mpeg"},
		{"ogg", append([]byte("OggS"), make([]byte, 16)...), "audio/ogg"},
		{"flac", append([]byte("fLaC"), make([]byte, 16)...), "audio/flac"},
		{"m4a", []byte("\x00\x00\x00\x18ftypM4A \x00\x00\x00\x00"), ""},
		{"unknown", []byte("not audio at all"), ""},
	}
	for _, c := range cases {
		if got := mimeOf(c.head); got != c.want {
			t.Errorf("%s MIME = %q, want %q", c.name, got, c.want)
		}
	}

	t.Run("80mb-mp3-chunks-internally", func(t *testing.T) {
		dir := t.TempDir()
		w, err := NewWhisper(fakeWhisper(t, dir, oneSegment), fakeModel(t, dir))
		if err != nil {
			t.Fatal(err)
		}
		frame := make([]byte, 417)
		copy(frame, []byte{0xFF, 0xFB, 0x90, 0x64})
		frames := (80 << 20) / len(frame)
		audio := bytes.Repeat(frame, frames)
		tr, err := w.Transcribe(t.Context(), bytes.NewReader(audio), Options{MaxBytes: MaxAudioBytes})
		if err != nil {
			t.Fatalf("80 MB MP3: %v", err)
		}
		if tr.MIME != "audio/mpeg" || tr.Chunks < 8 || tr.Chunks > MaxChunks {
			t.Fatalf("transcript = mime %q chunks %d", tr.MIME, tr.Chunks)
		}
		if tr.Text == "" {
			t.Fatal("chunked MP3 produced no text")
		}
	})

	t.Run("m4a-without-ffmpeg", func(t *testing.T) {
		dir := t.TempDir()
		w, err := NewWhisper(fakeWhisper(t, dir, oneSegment), fakeModel(t, dir))
		if err != nil {
			t.Fatal(err)
		}
		m4a := []byte("\x00\x00\x00\x18ftypM4A \x00\x00\x00\x00trailing")
		if _, err := w.Transcribe(t.Context(), bytes.NewReader(m4a), Options{}); !errors.Is(err, ErrContainerUnavailable) {
			t.Fatalf("M4A container = %v, want ErrContainerUnavailable", err)
		}
	})
}
