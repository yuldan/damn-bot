package backup

import (
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"

	"github.com/damnbot/damnbot/internal/secret"
)

const (
	magic         = "DMBK1\x00"
	formatVersion = 1
	chunkSize     = 64 << 10
	headerMax     = 4096
	keySize       = 32
	tagSize       = 16
	nonceSize     = 24
	prefixSize    = 16
)

const (
	wrapX25519 = "x25519"
	algXChaCha = "xchacha20poly1305"
)

// Header is the public, authenticated metadata of an archive. It is readable
// without the key so `sessions backup ls` can list archives.
type Header struct {
	Version        int
	CreatedAt      int64 // Unix seconds
	App            string
	IncludeSecrets bool
	Chunk          int
	RecipientID    string
	ManifestSHA256 string // lowercase hex, empty when no manifest was written
}

type wireHeader struct {
	V              int    `json:"v"`
	Wrap           string `json:"wrap"`
	Alg            string `json:"alg"`
	Ephemeral      string `json:"ephemeral"`
	Recipient      string `json:"recipient"`
	WrapNonce      string `json:"wrap_nonce"`
	WrappedDEK     string `json:"wrapped_dek"`
	NoncePrefix    string `json:"nonce_prefix"`
	HeaderNonce    string `json:"header_nonce"`
	Chunk          int    `json:"chunk"`
	CreatedAt      int64  `json:"created_at"`
	App            string `json:"app"`
	IncludeSecrets bool   `json:"include_secrets"`
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
}

func headerAAD(hlen uint16, raw []byte) []byte {
	a := make([]byte, 0, len(magic)+3+len(raw))
	a = append(a, magic...)
	a = append(a, byte(formatVersion))
	a = binary.BigEndian.AppendUint16(a, hlen)
	a = append(a, raw...)
	return a
}

func dekAAD(h *wireHeader) []byte {
	a := make([]byte, 0, len(magic)+1+len(h.Recipient)+len(h.Ephemeral)+len(h.WrapNonce))
	a = append(a, magic...)
	a = append(a, byte(formatVersion))
	a = append(a, h.Recipient...)
	a = append(a, h.Ephemeral...)
	a = append(a, h.WrapNonce...)
	return a
}

// derive returns a 32-byte HKDF-SHA256 subkey.
func derive(shared []byte, info string, bind []byte) ([]byte, error) {
	k := make([]byte, keySize)
	label := append(append([]byte(info), 0), bind...)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, nil, label), k); err != nil {
		return nil, fmt.Errorf("backup: derive %s: %w", info, err)
	}
	return k, nil
}

func curve25519Pub(priv []byte) ([]byte, error) {
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKey, err)
	}
	return pub, nil
}

func sharedSecret(priv, pub []byte) ([]byte, error) {
	if len(priv) != keySize || len(pub) != keySize {
		return nil, fmt.Errorf("%w: X25519 key length", ErrKey)
	}
	s, err := curve25519.X25519(priv, pub)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKey, err)
	}
	return s, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	a, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("backup: AEAD: %w", err)
	}
	return a, nil
}

func decodeB64(s string, size int) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != size {
		return nil, fmt.Errorf("%w: base64 field", ErrCorrupt)
	}
	return b, nil
}

// WriterOptions are the authenticated metadata of a new archive.
type WriterOptions struct {
	CreatedAt      time.Time
	App            string
	IncludeSecrets bool
	ManifestSHA256 string // lowercase hex of manifest.json, optional
	Rand           io.Reader
}

// buildWireHeader generates the DEK and every random field of a new header.
func buildWireHeader(key secret.BackupKey, o *WriterOptions) (*wireHeader, []byte, []byte, []byte, error) {
	if err := validateHeaderOptions(key, o); err != nil {
		return nil, nil, nil, nil, err
	}
	rnd := o.Rand
	if rnd == nil {
		rnd = rand.Reader
	}
	ephPriv, dek, prefix, wrapNonce, headerNonce, err := headerRandoms(rnd)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	ephPub, err := curve25519Pub(ephPriv)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	shared, err := sharedSecret(ephPriv, key.Public)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	wh := &wireHeader{
		V: formatVersion, Wrap: wrapX25519, Alg: algXChaCha,
		Ephemeral:      base64.StdEncoding.EncodeToString(ephPub),
		Recipient:      key.ID,
		WrapNonce:      base64.StdEncoding.EncodeToString(wrapNonce),
		NoncePrefix:    base64.StdEncoding.EncodeToString(prefix),
		HeaderNonce:    base64.StdEncoding.EncodeToString(headerNonce),
		Chunk:          chunkSize,
		CreatedAt:      o.CreatedAt.UTC().Unix(),
		App:            o.App,
		IncludeSecrets: o.IncludeSecrets,
		ManifestSHA256: o.ManifestSHA256,
	}
	if wh.CreatedAt == 0 {
		wh.CreatedAt = time.Now().UTC().Unix()
	}
	bind := append(append([]byte{}, ephPub...), key.Public...)
	if err := wrapDEK(wh, shared, dek, bind); err != nil {
		return nil, nil, nil, nil, err
	}
	return wh, shared, dek, prefix, nil
}

func validateHeaderOptions(key secret.BackupKey, o *WriterOptions) error {
	if len(key.Private) != keySize || len(key.Public) != keySize || key.ID == "" {
		return fmt.Errorf("%w: backup key", ErrKey)
	}
	if o.ManifestSHA256 != "" {
		if _, err := hex.DecodeString(o.ManifestSHA256); err != nil || len(o.ManifestSHA256) != 2*sha256.Size {
			return fmt.Errorf("backup: manifest sha256: %w", ErrCorrupt)
		}
	}
	return nil
}

func headerRandoms(rnd io.Reader) (ephPriv, dek, prefix, wrapNonce, headerNonce []byte, err error) {
	if ephPriv, err = random(rnd, keySize); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if dek, err = random(rnd, keySize); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if prefix, err = random(rnd, prefixSize); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if wrapNonce, err = random(rnd, nonceSize); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if headerNonce, err = random(rnd, nonceSize); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	return ephPriv, dek, prefix, wrapNonce, headerNonce, nil
}

// emitHeader authenticates and writes the canonical header.
//
//nolint:gosec // G115: header length is bounded by headerMax in validateHeaderOptions
func emitHeader(w io.Writer, wh *wireHeader, shared, bind []byte) error {
	raw, err := json.Marshal(wh)
	if err != nil {
		return fmt.Errorf("backup: encode header: %w", err)
	}
	if len(raw) > headerMax {
		return fmt.Errorf("%w: header too large", ErrCorrupt)
	}
	hkey, err := derive(shared, "dmbk1/header", bind)
	if err != nil {
		return err
	}
	haead, err := newAEAD(hkey)
	if err != nil {
		return err
	}
	nonce, err := decodeB64(wh.HeaderNonce, nonceSize)
	if err != nil {
		return err
	}
	tag := haead.Seal(nil, nonce, nil, headerAAD(uint16(len(raw)), raw))
	head := make([]byte, 0, len(magic)+3+len(raw)+tagSize)
	head = append(head, magic...)
	head = append(head, byte(formatVersion))
	head = binary.BigEndian.AppendUint16(head, uint16(len(raw)))
	head = append(head, raw...)
	head = append(head, tag...)
	return writeAll(w, head)
}

// writeHeader emits the header and returns the payload key, nonce prefix and
// authenticated header hash.
func writeHeader(w io.Writer, key secret.BackupKey, o *WriterOptions) (prefix, hash, dek []byte, err error) {
	wh, shared, dek, prefix, err := buildWireHeader(key, o)
	if err != nil {
		return nil, nil, nil, err
	}
	epub, derr := decodeB64(wh.Ephemeral, keySize)
	if derr != nil {
		return nil, nil, nil, derr
	}
	bind := append(append([]byte{}, epub...), key.Public...)
	if eerr := emitHeader(w, wh, shared, bind); eerr != nil {
		return nil, nil, nil, eerr
	}
	raw, err := json.Marshal(wh)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("backup: encode header: %w", err)
	}
	clear(shared)
	h := sha256.Sum256(raw)
	return prefix, h[:], dek, nil
}

func wrapDEK(wh *wireHeader, shared, dek, bind []byte) error {
	wkey, err := derive(shared, "dmbk1/wrap", bind)
	if err != nil {
		return err
	}
	waead, err := newAEAD(wkey)
	if err != nil {
		return err
	}
	nonce, err := decodeB64(wh.WrapNonce, nonceSize)
	if err != nil {
		return err
	}
	wh.WrappedDEK = base64.StdEncoding.EncodeToString(waead.Seal(nil, nonce, dek, dekAAD(wh)))
	return nil
}

func random(r io.Reader, n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, fmt.Errorf("backup: random: %w", err)
	}
	return b, nil
}

// uint32Len narrows a length the callers bound before it reaches the wire
// (manifest ≤ 16 MiB in writePayload, chunk buffer ≤ chunkSize in flush).
//
//nolint:gosec // G115: every caller validates its own bound before calling
func uint32Len(n int) uint32 { return uint32(n) }

func writeAll(w io.Writer, b []byte) error {
	if _, err := w.Write(b); err != nil {
		return fmt.Errorf("backup: write: %w", err)
	}
	return nil
}
