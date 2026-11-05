//go:build unix

package stt

import (
	"io"
	"io/fs"
	"syscall"
	"testing"
)

// etxtbsyAfterData serves its payload, then reports ETXTBSY instead of EOF
// once. That is the ordering Transcribe sees when the spool read races the
// fixture writer: bytes are consumed first, the kernel refuses exec after.
// Seek rewinds the payload so a retried attempt can read it again.
type etxtbsyAfterData struct {
	data  []byte
	pos   int
	fired bool
}

func (r *etxtbsyAfterData) Seek(int64, int) (int64, error) {
	r.pos = 0
	return 0, nil
}

func (r *etxtbsyAfterData) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		if !r.fired {
			r.fired = true
			return 0, &fs.PathError{Op: "read", Path: "audio.wav", Err: syscall.ETXTBSY}
		}
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// TestWhisperTranscribeRetriesAfterETXTBSY pins the helper contract: a retry
// after ETXTBSY must rewind the spooled reader. Without the rewind the second
// attempt reads EOF and the real ETXTBSY is masked as ErrEmptyAudio.
func TestWhisperTranscribeRetriesAfterETXTBSY(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w, err := NewWhisper(fakeWhisper(t, dir, oneSegment), fakeModel(t, dir))
	if err != nil {
		t.Fatalf("NewWhisper: %v", err)
	}
	src := &etxtbsyAfterData{data: wavBytes(t, 8000, 800)}
	tr, err := transcribe(t.Context(), t, w, src, Options{})
	if err != nil {
		t.Fatalf("transcribe: %v (retry must rewind, not report empty audio)", err)
	}
	if tr.Text != "hello world" || tr.Chunks != 1 {
		t.Errorf("transcript = %+v", tr)
	}
	if !src.fired {
		t.Error("fixture never injected ETXTBSY; test does not exercise the retry")
	}
	if src.pos != len(src.data) {
		t.Errorf("retried attempt read %d of %d bytes: reader was not rewound", src.pos, len(src.data))
	}
}
