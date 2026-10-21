package backup

import (
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/damnbot/damnbot/internal/secret"
)

// Reader decrypts one DMBK1 payload. NewReader authenticates the header, so a
// wrong key or a modified header fails before any payload byte is read.
type Reader struct {
	r       io.Reader
	aead    cipher.AEAD
	prefix  []byte
	hash    []byte
	n       uint64
	buf     []byte
	final   bool
	checked bool
	err     error
}

// NewReader parses and authenticates the header and returns the metadata plus
// a streaming plaintext reader. Errors are typed: ErrAuth for a wrong key or
// modified header, ErrIncompatible for an unknown format, ErrCorrupt for
// malformed framing.
func NewReader(r io.Reader, key secret.BackupKey) (Header, *Reader, error) {
	h, dek, prefix, hash, err := readHeader(r, key)
	if err != nil {
		return Header{}, nil, err
	}
	aead, err := newAEAD(dek)
	if err != nil {
		return Header{}, nil, err
	}
	return h, &Reader{r: r, aead: aead, prefix: prefix, hash: hash}, nil
}

// Read returns decrypted payload bytes. It returns io.EOF after the final
// authenticated chunk.
func (r *Reader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		if r.final {
			if err := r.checkTrailing(); err != nil {
				r.err = err
				return 0, err
			}
			r.err = io.EOF
			return 0, io.EOF
		}
		if err := r.readChunk(); err != nil {
			r.err = err
			return 0, err
		}
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

// checkTrailing rejects bytes after the authenticated final chunk.
func (r *Reader) checkTrailing() error {
	if r.checked {
		return nil
	}
	r.checked = true
	var one [1]byte
	n, err := io.ReadFull(r.r, one[:])
	if n > 0 {
		return fmt.Errorf("%w: trailing data", ErrCorrupt)
	}
	if err != nil && err != io.EOF {
		return fmt.Errorf("%w: trailing data: %w", ErrCorrupt, err)
	}
	return nil
}

func (r *Reader) readChunk() error {
	var lb [4]byte
	if _, err := io.ReadFull(r.r, lb[:]); err != nil {
		return fmt.Errorf("%w: chunk header: %w", ErrTruncated, err)
	}
	hdr := binary.BigEndian.Uint32(lb[:])
	final := hdr&(1<<31) != 0
	n := hdr &^ (1 << 31)
	if n > chunkSize {
		return fmt.Errorf("%w: chunk length %d", ErrCorrupt, n)
	}
	if n == 0 && !final {
		return fmt.Errorf("%w: empty non-final chunk", ErrCorrupt)
	}
	ct := make([]byte, int(n)+tagSize)
	if _, err := io.ReadFull(r.r, ct); err != nil {
		return fmt.Errorf("%w: chunk: %w", ErrTruncated, err)
	}
	pt, err := r.aead.Open(nil, chunkNonce(r.prefix, r.n), ct, chunkAAD(r.hash, r.n, n, final))
	if err != nil {
		return fmt.Errorf("%w: chunk tag %d", ErrCorrupt, r.n)
	}
	r.n++
	r.final = final
	r.buf = pt
	return nil
}
