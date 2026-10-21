package secret

import (
	"context"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	storageKeyFile = "storage.key"
	envelopeVer    = 1
	codecNonce     = 24
)

// Codec implements ctxdb.Codec with XChaCha20-Poly1305 and per-scope HKDF subkeys derived from one database DEK (ADR-124).
type Codec struct {
	dek  []byte
	id   string
	mu   sync.Mutex
	keys map[string]cipher.AEAD
}

// NewCodec builds a codec from a 32-byte DEK.
func NewCodec(dek []byte) (*Codec, error) {
	if len(dek) != KeySize {
		return nil, fmt.Errorf("secret: DEK length: %w", ErrKeyFile)
	}
	k := append([]byte(nil), dek...)
	return &Codec{dek: k, id: keyID(k), keys: map[string]cipher.AEAD{}}, nil
}

// ID identifies the DEK (recorded in the DB marker).
func (c *Codec) ID() string { return c.id }

func (c *Codec) aead(scope string) (cipher.AEAD, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if a, ok := c.keys[scope]; ok {
		return a, nil
	}
	key := make([]byte, KeySize)
	info := append([]byte("dmbot/at-rest/v1/"), scope...)
	if _, err := io.ReadFull(hkdf.New(sha256.New, c.dek, nil, info), key); err != nil {
		return nil, fmt.Errorf("secret: derive %s: %w", scope, err)
	}
	a, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("secret: codec: %w", err)
	}
	c.keys[scope] = a
	return a, nil
}

// Seal encrypts plaintext with a random nonce and binds scope as AAD over version||nonce||ciphertext.
func (c *Codec) Seal(scope string, plaintext []byte) ([]byte, error) {
	a, err := c.aead(scope)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, codecNonce)
	if _, rerr := rand.Read(nonce); rerr != nil {
		return nil, fmt.Errorf("secret: nonce: %w", rerr)
	}
	out := make([]byte, 1+codecNonce, 1+codecNonce+len(plaintext)+a.Overhead())
	out[0] = envelopeVer
	copy(out[1:], nonce)
	return a.Seal(out, nonce, plaintext, []byte(scope)), nil
}

// Open decrypts an envelope produced by Seal.
func (c *Codec) Open(scope string, sealed []byte) ([]byte, error) {
	if len(sealed) < 1+codecNonce || sealed[0] != envelopeVer {
		return nil, fmt.Errorf("%w: envelope", ErrKeyFile)
	}
	a, err := c.aead(scope)
	if err != nil {
		return nil, err
	}
	pt, err := a.Open(nil, sealed[1:1+codecNonce], sealed[1+codecNonce:], []byte(scope))
	if err != nil {
		return nil, fmt.Errorf("%w: decrypt", ErrKeyFile)
	}
	return pt, nil
}

// Hash is a keyed hash for the event hash chain.
func (c *Codec) Hash(data string) string {
	key := make([]byte, KeySize)
	if _, err := io.ReadFull(hkdf.New(sha256.New, c.dek, nil, []byte("dmbot/at-rest/v1/event-hash")), key); err != nil {
		return ""
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return hex.EncodeToString(m.Sum(nil))
}

// Zero wipes the DEK.
func (c *Codec) Zero() {
	c.mu.Lock()
	clear(c.dek)
	c.dek = nil
	c.keys = nil
	c.mu.Unlock()
}

type storageKeyJSON struct {
	V       int    `json:"v"`
	Source  Source `json:"source"`
	KEKID   string `json:"kek_id"`
	Nonce   string `json:"nonce"`
	Wrapped string `json:"wrapped_dek"`
}

// StorageKeyPath is <data_dir>/storage.key.
func StorageKeyPath(dataDir string) string { return filepath.Join(dataDir, storageKeyFile) }

// OpenCodec loads the database DEK, or provisions one when create is true.
// The wrapping KEK id is stored with the wrapped DEK (SEC7-15), so rotation
// never depends on caller ordering.
func OpenCodec(ctx context.Context, dataDir string, st *Store, create bool) (*Codec, error) {
	if !create {
		dek, err := loadStorageKey(ctx, dataDir, st)
		if err != nil {
			return nil, err
		}
		return NewCodec(dek)
	}
	if _, serr := os.Stat(StorageKeyPath(dataDir)); serr == nil {
		loaded, lerr := loadStorageKey(ctx, dataDir, st)
		if lerr != nil {
			return nil, lerr
		}
		return NewCodec(loaded)
	} else if !errors.Is(serr, fs.ErrNotExist) {
		return nil, fmt.Errorf("secret: storage key: %w", serr)
	}
	kek, err := st.EnsureAtRest(ctx)
	if err != nil {
		return nil, err
	}
	defer kek.Zero()
	dek, err := st.randomKey()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, codecNonce)
	if _, rerr := rand.Read(nonce); rerr != nil {
		return nil, fmt.Errorf("secret: nonce: %w", rerr)
	}
	a, err := chacha20poly1305.NewX(kek.Key)
	if err != nil {
		return nil, fmt.Errorf("secret: codec: %w", err)
	}
	wrapped := a.Seal(nil, nonce, dek, storageAAD(kek.Source, kek.ID))
	raw, err := json.Marshal(storageKeyJSON{
		V: 1, Source: kek.Source, KEKID: kek.ID,
		Nonce: base64.StdEncoding.EncodeToString(nonce), Wrapped: base64.StdEncoding.EncodeToString(wrapped),
	})
	if err != nil {
		return nil, fmt.Errorf("secret: encode storage key: %w", err)
	}
	if werr := writeStorageKey(dataDir, raw); werr != nil {
		if !errors.Is(werr, fs.ErrExist) {
			return nil, werr
		}
		loaded, lerr := loadStorageKey(ctx, dataDir, st)
		if lerr != nil {
			return nil, lerr
		}
		return NewCodec(loaded)
	}
	return NewCodec(dek)
}

func storageAAD(src Source, id string) []byte {
	return []byte("dmbot/storage-key/v1|" + string(src) + "|" + id)
}

func loadStorageKey(ctx context.Context, dataDir string, st *Store) ([]byte, error) {
	raw, err := readStorageKey(dataDir)
	if err != nil {
		return nil, err
	}
	var f storageKeyJSON
	if uerr := json.Unmarshal(raw, &f); uerr != nil || f.V != 1 {
		return nil, fmt.Errorf("secret: storage key JSON: %w", ErrKeyFile)
	}
	kek, err := st.AtRest(ctx, f.Source, f.KEKID)
	if err != nil {
		return nil, err
	}
	defer kek.Zero()
	nonce, err := base64.StdEncoding.DecodeString(f.Nonce)
	if err != nil || len(nonce) != codecNonce {
		return nil, fmt.Errorf("secret: storage nonce: %w", ErrKeyFile)
	}
	wrapped, err := base64.StdEncoding.DecodeString(f.Wrapped)
	if err != nil || len(wrapped) != KeySize+16 {
		return nil, fmt.Errorf("secret: wrapped DEK: %w", ErrKeyFile)
	}
	a, err := chacha20poly1305.NewX(kek.Key)
	if err != nil {
		return nil, fmt.Errorf("secret: codec: %w", err)
	}
	dek, err := a.Open(nil, nonce, wrapped, storageAAD(f.Source, f.KEKID))
	if err != nil {
		return nil, fmt.Errorf("%w: unwrap storage key", ErrKeyFile)
	}
	return dek, nil
}

func readStorageKey(dataDir string) (b []byte, err error) {
	path := StorageKeyPath(dataDir)
	li, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("secret: storage key: %w", err)
	}
	if li.Mode()&os.ModeSymlink != 0 || !li.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: storage key not regular", ErrKeyFile)
	}
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("secret: read storage key: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("secret: stat storage key: %w", err)
	}
	if !os.SameFile(li, fi) {
		return nil, fmt.Errorf("%w: storage key changed while opening", ErrKeyFile)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: storage key mode %04o", ErrPermissions, fi.Mode().Perm())
	}
	b, err = io.ReadAll(io.LimitReader(f, 8<<10))
	if err != nil {
		return nil, fmt.Errorf("secret: read storage key: %w", err)
	}
	if len(b) > 8<<10 {
		return nil, fmt.Errorf("%w: storage key too large", ErrKeyFile)
	}
	return b, nil
}

func writeStorageKey(dataDir string, raw []byte) (err error) {
	if merr := os.MkdirAll(dataDir, 0o700); merr != nil {
		return fmt.Errorf("secret: data dir: %w", merr)
	}
	path := StorageKeyPath(dataDir)
	suffix, err := randBytes(8)
	if err != nil {
		return err
	}
	tmp := path + ".tmp-" + hex.EncodeToString(suffix)
	f, err := openKeyPath(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(raw)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		return fmt.Errorf("secret: write storage key: %w", errors.Join(err, removeTemp(tmp)))
	}
	if err := os.Link(tmp, path); err != nil {
		lerr := errors.Join(err, removeTemp(tmp))
		if errors.Is(lerr, fs.ErrExist) {
			return fs.ErrExist
		}
		return fmt.Errorf("secret: publish storage key: %w", lerr)
	}
	if err := removeTemp(tmp); err != nil {
		return err
	}
	return syncDir(dataDir)
}

func randBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("secret: random: %w", err)
	}
	return b, nil
}

func removeTemp(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("secret: remove %s: %w", path, err)
	}
	return nil
}

// openKeyPath opens one key-store path. Create mode keeps the 0600 contract in
// a single place, and no caller passes a request-supplied path.
//
//nolint:gosec // G304: paths come from the operator's config/data dir
func openKeyPath(path string, flag int, perm os.FileMode) (*os.File, error) {
	var (
		f   *os.File
		err error
	)
	if flag&os.O_CREATE != 0 {
		f, err = os.OpenFile(path, flag, perm)
	} else {
		f, err = os.Open(path)
	}
	if err != nil {
		return nil, fmt.Errorf("secret: open %s: %w", path, err)
	}
	return f, nil
}

func syncDir(dir string) error {
	f, err := openKeyPath(dir, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	serr := f.Sync()
	cerr := f.Close()
	if runtime.GOOS == "windows" {
		return nil
	}
	return errors.Join(serr, cerr)
}
