package backup

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/damnbot/damnbot/internal/secret"
)

func decodeHeader(raw []byte) (wireHeader, error) {
	var h wireHeader
	if err := json.Unmarshal(raw, &h); err != nil {
		return h, fmt.Errorf("%w: header JSON", ErrCorrupt)
	}
	if h.V != formatVersion || h.Wrap != wrapX25519 || h.Alg != algXChaCha || h.Chunk != chunkSize {
		return h, fmt.Errorf("%w: v=%d wrap=%q alg=%q chunk=%d", ErrIncompatible, h.V, h.Wrap, h.Alg, h.Chunk)
	}
	return h, nil
}

func publicHeader(h *wireHeader) (Header, error) {
	if h.ManifestSHA256 != "" {
		if _, err := hex.DecodeString(h.ManifestSHA256); err != nil || len(h.ManifestSHA256) != 2*sha256.Size {
			return Header{}, fmt.Errorf("%w: manifest sha256", ErrCorrupt)
		}
	}
	return Header{
		Version: h.V, CreatedAt: h.CreatedAt, App: h.App, IncludeSecrets: h.IncludeSecrets,
		Chunk: h.Chunk, RecipientID: h.Recipient, ManifestSHA256: h.ManifestSHA256,
	}, nil
}

// readRawHeader reads magic, version, header JSON and the header tag.
func readRawHeader(r io.Reader) (uint16, []byte, []byte, error) {
	m := make([]byte, len(magic))
	if _, err := io.ReadFull(r, m); err != nil {
		return 0, nil, nil, fmt.Errorf("%w: magic: %w", ErrTruncated, err)
	}
	if string(m) != magic {
		return 0, nil, nil, fmt.Errorf("%w: magic", ErrIncompatible)
	}
	var ver [1]byte
	if _, err := io.ReadFull(r, ver[:]); err != nil {
		return 0, nil, nil, fmt.Errorf("%w: version: %w", ErrTruncated, err)
	}
	if ver[0] != formatVersion {
		return 0, nil, nil, fmt.Errorf("%w: version %d", ErrIncompatible, ver[0])
	}
	var lb [2]byte
	if _, err := io.ReadFull(r, lb[:]); err != nil {
		return 0, nil, nil, fmt.Errorf("%w: header length: %w", ErrTruncated, err)
	}
	hlen := binary.BigEndian.Uint16(lb[:])
	raw, tag, err := readHeaderBody(r, hlen)
	return hlen, raw, tag, err
}

func readHeaderBody(r io.Reader, hlen uint16) ([]byte, []byte, error) {
	if hlen == 0 || int(hlen) > headerMax {
		return nil, nil, fmt.Errorf("%w: header length %d", ErrCorrupt, hlen)
	}
	raw := make([]byte, hlen)
	if _, err := io.ReadFull(r, raw); err != nil {
		return nil, nil, fmt.Errorf("%w: header: %w", ErrTruncated, err)
	}
	tag := make([]byte, tagSize)
	if _, err := io.ReadFull(r, tag); err != nil {
		return nil, nil, fmt.Errorf("%w: header tag: %w", ErrTruncated, err)
	}
	return raw, tag, nil
}

// openHeader decodes the header and verifies its tag under key.
func openHeader(hlen uint16, raw, tag []byte, key secret.BackupKey) (*wireHeader, []byte, error) {
	h, err := decodeHeader(raw)
	if err != nil {
		return nil, nil, err
	}
	epub, err := decodeB64(h.Ephemeral, keySize)
	if err != nil {
		return nil, nil, err
	}
	bind := append(append([]byte{}, epub...), key.Public...)
	shared, err := sharedSecret(key.Private, epub)
	if err != nil {
		return nil, nil, err
	}
	hkey, err := derive(shared, "dmbk1/header", bind)
	if err != nil {
		return nil, nil, err
	}
	aaead, err := newAEAD(hkey)
	if err != nil {
		return nil, nil, err
	}
	hn, err := decodeB64(h.HeaderNonce, nonceSize)
	if err != nil {
		return nil, nil, err
	}
	if _, oerr := aaead.Open(nil, hn, tag, headerAAD(hlen, raw)); oerr != nil {
		return nil, nil, fmt.Errorf("%w: header tag", ErrAuth)
	}
	if h.Recipient != key.ID {
		return nil, nil, fmt.Errorf("%w: archive is for %s", ErrAuth, h.Recipient)
	}
	return &h, shared, nil
}

func unwrapDEK(h *wireHeader, shared, bind []byte) ([]byte, error) {
	wn, err := decodeB64(h.WrapNonce, nonceSize)
	if err != nil {
		return nil, err
	}
	wrapped, err := base64.StdEncoding.DecodeString(h.WrappedDEK)
	if err != nil || len(wrapped) != keySize+tagSize {
		return nil, fmt.Errorf("%w: wrapped DEK", ErrCorrupt)
	}
	wkey, err := derive(shared, "dmbk1/wrap", bind)
	if err != nil {
		return nil, err
	}
	waead, err := newAEAD(wkey)
	if err != nil {
		return nil, err
	}
	dek, err := waead.Open(nil, wn, wrapped, dekAAD(h))
	if err != nil {
		return nil, fmt.Errorf("%w: wrapped DEK", ErrAuth)
	}
	return dek, nil
}

// readHeader parses and authenticates the header, then returns the payload
// AEAD key, nonce prefix and header hash (bound into every chunk AAD).
func readHeader(r io.Reader, key secret.BackupKey) (Header, []byte, []byte, []byte, error) {
	hlen, raw, tag, err := readRawHeader(r)
	if err != nil {
		return Header{}, nil, nil, nil, err
	}
	h, shared, err := openHeader(hlen, raw, tag, key)
	if err != nil {
		return Header{}, nil, nil, nil, err
	}
	epub, derr := decodeB64(h.Ephemeral, keySize)
	if derr != nil {
		return Header{}, nil, nil, nil, derr
	}
	bind := append(append([]byte{}, epub...), key.Public...)
	dek, err := unwrapDEK(h, shared, bind)
	if err != nil {
		return Header{}, nil, nil, nil, err
	}
	prefix, err := decodeB64(h.NoncePrefix, prefixSize)
	if err != nil {
		return Header{}, nil, nil, nil, err
	}
	pub, err := publicHeader(h)
	if err != nil {
		return Header{}, nil, nil, nil, err
	}
	clear(shared)
	hash := sha256.Sum256(raw)
	return pub, dek, prefix, hash[:], nil
}

func chunkAAD(hash []byte, counter uint64, n uint32, final bool) []byte {
	a := make([]byte, 0, sha256.Size+13)
	a = append(a, hash...)
	a = binary.BigEndian.AppendUint64(a, counter)
	a = binary.BigEndian.AppendUint32(a, n)
	if final {
		a = append(a, 1)
	} else {
		a = append(a, 0)
	}
	return a
}

func chunkNonce(prefix []byte, counter uint64) []byte {
	n := make([]byte, nonceSize)
	copy(n, prefix)
	binary.BigEndian.PutUint64(n[prefixSize:], counter)
	return n
}
