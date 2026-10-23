package backup

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/secret"
)

func writeArchive(t *testing.T, key secret.BackupKey, payload []byte, o *WriterOptions) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, key, o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRoundTrip(t *testing.T) {
	key := testKey(t)
	created := time.Unix(1_700_000_000, 0).UTC()
	sizes := []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 2*chunkSize + 123}
	for _, n := range sizes {
		payload := make([]byte, n)
		if _, err := rand.Read(payload); err != nil {
			t.Fatal(err)
		}
		raw := writeArchive(t, key, payload, &WriterOptions{CreatedAt: created, App: "test", IncludeSecrets: true, ManifestSHA256: strings.Repeat("a", 64)})
		h, r, err := NewReader(bytes.NewReader(raw), key)
		if err != nil {
			t.Fatalf("size %d: NewReader: %v", n, err)
		}
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("size %d: ReadAll: %v", n, err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("size %d: payload differs", n)
		}
		if h.CreatedAt != created.Unix() || !h.IncludeSecrets || h.App != "test" || h.RecipientID != key.ID || h.Chunk != chunkSize {
			t.Fatalf("size %d: header %+v", n, h)
		}
	}
}

func TestWrongKey(t *testing.T) {
	key := testKey(t)
	other := testKey(t)
	raw := writeArchive(t, key, []byte("secret"), &WriterOptions{})
	if _, _, err := NewReader(bytes.NewReader(raw), other); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong key = %v, want ErrAuth", err)
	}
}

func TestTamperEveryByte(t *testing.T) {
	key := testKey(t)
	raw := writeArchive(t, key, bytes.Repeat([]byte("x"), 100), &WriterOptions{})
	for i := range raw {
		bad := append([]byte(nil), raw...)
		bad[i] ^= 0x01
		h, r, err := NewReader(bytes.NewReader(bad), key)
		if err == nil {
			_, err = io.ReadAll(r)
			_ = h
		}
		if err == nil {
			t.Fatalf("byte %d: tampered archive accepted", i)
		}
	}
}

func TestTruncated(t *testing.T) {
	key := testKey(t)
	raw := writeArchive(t, key, bytes.Repeat([]byte("y"), 100), &WriterOptions{})
	for i := range raw {
		_, r, err := NewReader(bytes.NewReader(raw[:i]), key)
		if err == nil {
			_, err = io.ReadAll(r)
		}
		if err == nil {
			t.Fatalf("truncated at %d/%d accepted", i, len(raw))
		}
	}
}

func TestTrailingRejected(t *testing.T) {
	key := testKey(t)
	raw := append(writeArchive(t, key, []byte("z"), &WriterOptions{}), 'x')
	if _, r, err := NewReader(bytes.NewReader(raw), key); err == nil {
		if _, err = io.ReadAll(r); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("trailing = %v, want ErrCorrupt", err)
		}
	}
}

func TestManifestSHAValidation(t *testing.T) {
	key := testKey(t)
	var buf bytes.Buffer
	if _, err := NewWriter(&buf, key, &WriterOptions{ManifestSHA256: "not-hex"}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("bad manifest = %v", err)
	}
}

func TestEmptyFinalChunk(t *testing.T) {
	key := testKey(t)
	raw := writeArchive(t, key, nil, &WriterOptions{})
	h, r, err := NewReader(bytes.NewReader(raw), key)
	if err != nil || h.Version != formatVersion {
		t.Fatalf("NewReader = %+v, %v", h, err)
	}
	if n, err := r.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("Read empty = %d, %v", n, err)
	}
}

func TestCloseIdempotent(t *testing.T) {
	key := testKey(t)
	var buf bytes.Buffer
	w, err := NewWriter(&buf, key, &WriterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
}

func TestReadMetaRejectsBadRecipientBytes(t *testing.T) {
	key := testKey(t)
	var buf bytes.Buffer
	if _, _, _, err := writeHeader(&buf, key, &WriterOptions{}); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(nil), buf.Bytes()...)
	i := bytes.Index(raw, []byte(key.ID))
	if i < 0 {
		t.Fatal("recipient id not found in header")
	}
	copy(raw[i:i+32], bytes.Repeat([]byte("z"), 32))
	if _, err := ReadMeta(bytes.NewReader(raw)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("ReadMeta accepted bad recipient id: %v", err)
	}
}
