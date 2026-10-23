package backup

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/damnbot/damnbot/internal/secret"
)

const manifestMax = 16 << 20

// Verify decrypts and authenticates the whole archive without writing
// anything, and returns its header and manifest.
func Verify(ctx context.Context, path string, key secret.BackupKey) (hdr Header, man Manifest, err error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return Header{}, Manifest{}, fmt.Errorf("backup: open %s: %w", path, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	h, r, err := NewReader(f, key)
	if err != nil {
		return Header{}, Manifest{}, err
	}
	m, err := readAndCheckManifest(ctx, r, &h)
	if err != nil {
		return Header{}, Manifest{}, err
	}
	for _, e := range m.Entries {
		if err := ctx.Err(); err != nil {
			return Header{}, Manifest{}, fmt.Errorf("backup: %w", err)
		}
		got, err := hashN(r, e.Size)
		if err != nil {
			return Header{}, Manifest{}, err
		}
		if got != e.SHA256 {
			return Header{}, Manifest{}, fmt.Errorf("%w: entry %s hash", ErrCorrupt, e.Path)
		}
	}
	var one [1]byte
	if n, err := r.Read(one[:]); n != 0 || !errors.Is(err, io.EOF) {
		return Header{}, Manifest{}, fmt.Errorf("%w: trailing payload", ErrCorrupt)
	}
	return h, m, nil
}

func readManifest(r io.Reader, h *Header) (Manifest, error) {
	body, err := readManifestBytes(r)
	if err != nil {
		return Manifest{}, err
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != h.ManifestSHA256 {
		return Manifest{}, fmt.Errorf("%w: manifest sha256", ErrCorrupt)
	}
	return decodeManifest(body)
}

func readManifestBytes(r io.Reader) ([]byte, error) {
	var lb [4]byte
	if _, err := io.ReadFull(r, lb[:]); err != nil {
		return nil, fmt.Errorf("%w: manifest length: %w", ErrTruncated, err)
	}
	n := binary.BigEndian.Uint32(lb[:])
	if n == 0 || n > manifestMax {
		return nil, fmt.Errorf("%w: manifest length %d", ErrCorrupt, n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, fmt.Errorf("%w: manifest: %w", ErrTruncated, err)
	}
	return b, nil
}

func readAndCheckManifest(ctx context.Context, r io.Reader, h *Header) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, fmt.Errorf("backup: %w", err)
	}
	return readManifest(r, h)
}

func hashN(r io.Reader, n int64) (string, error) {
	h := sha256.New()
	if _, err := io.CopyN(h, r, n); err != nil {
		return "", fmt.Errorf("%w: entry bytes: %w", ErrTruncated, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
