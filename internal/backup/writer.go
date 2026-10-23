package backup

import (
	"crypto/cipher"
	"encoding/binary"
	"io"

	"github.com/damnbot/damnbot/internal/secret"
)

// Writer encrypts one DMBK1 payload. It is not safe for concurrent use.
type Writer struct {
	w      io.Writer
	aead   cipher.AEAD
	prefix []byte
	hash   []byte
	buf    []byte
	n      uint64
	closed bool
	err    error
}

// NewWriter writes the authenticated header and returns a streaming writer.
// Every archive is encrypted; there is no plaintext option.
func NewWriter(w io.Writer, key secret.BackupKey, o *WriterOptions) (*Writer, error) {
	prefix, hash, dek, err := writeHeader(w, key, o)
	if err != nil {
		return nil, err
	}
	paead, err := newAEAD(dek)
	if err != nil {
		return nil, err
	}
	return &Writer{w: w, aead: paead, prefix: prefix, hash: hash, buf: make([]byte, 0, chunkSize)}, nil
}

// Write buffers plaintext and emits 64 KiB authenticated chunks.
func (w *Writer) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	total := len(p)
	for len(p) > 0 {
		n := min(chunkSize-len(w.buf), len(p))
		w.buf = append(w.buf, p[:n]...)
		p = p[n:]
		if len(w.buf) == chunkSize {
			if err := w.flush(false); err != nil {
				w.err = err
				return total - len(p), err
			}
		}
	}
	return total, nil
}

// Close writes the final chunk. It is idempotent.
func (w *Writer) Close() error {
	if w.closed {
		return w.err
	}
	w.closed = true
	if w.err == nil {
		w.err = w.flush(true)
	}
	return w.err
}

func (w *Writer) flush(final bool) error {
	n := uint32(len(w.buf)) //nolint:gosec // buffered len <= chunkSize
	ct := w.aead.Seal(nil, chunkNonce(w.prefix, w.n), w.buf, chunkAAD(w.hash, w.n, n, final))
	var lb [4]byte
	hdr := n
	if final {
		hdr |= 1 << 31
	}
	binary.BigEndian.PutUint32(lb[:], hdr)
	if err := writeAll(w.w, lb[:]); err != nil {
		return err
	}
	if err := writeAll(w.w, ct); err != nil {
		return err
	}
	w.n++
	w.buf = w.buf[:0]
	return nil
}
