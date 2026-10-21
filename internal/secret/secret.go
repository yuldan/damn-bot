// Package secret stores Damn-Bot's at-rest and backup keys (ADR-124). Keys
// live in the OS keychain when usable, otherwise in 0600 files below the
// profile config directory. It imports no internal package.
package secret

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

// KeySize is the length of every key material value.
const KeySize = 32

const (
	service       = "damnbot"
	atRestAccount = "at-rest"
	atRestPrefix  = "at-rest."
	atRestSuffix  = ".key"
	atRestMeta    = "at-rest.meta"
	fileMode      = 0o600
	keyringWait   = 10 * time.Second
)

// Source is where a key is stored.
type Source string

// Key sources.
const (
	SourceKeychain Source = "keychain"
	SourceFile     Source = "file"
)

// Typed errors (E-21).
var (
	ErrKeyMissing     = errors.New("secret: key not found")
	ErrKeyUnavailable = errors.New("secret: key store unavailable")
	ErrPermissions    = errors.New("secret: key path permissions are wider than 0600/0700")
	ErrKeyFile        = errors.New("secret: invalid key file")
	ErrSource         = errors.New("secret: unknown key source")
	ErrKeySource      = errors.New("secret: key source conflict")
)

// Keyring is the subset of the OS keychain this package uses.
type Keyring interface {
	Get(service, user string) (string, error)
	Set(service, user, value string) error
	Delete(service, user string) error
}
type osKeyring struct{}

func (osKeyring) Get(service, user string) (string, error) {
	v, err := keyring.Get(service, user)
	if err != nil {
		return "", fmt.Errorf("keyring: %w", err)
	}
	return v, nil
}

func (osKeyring) Set(service, user, value string) error {
	if err := keyring.Set(service, user, value); err != nil {
		return fmt.Errorf("keyring: %w", err)
	}
	return nil
}

func (osKeyring) Delete(service, user string) error {
	if err := keyring.Delete(service, user); err != nil {
		return fmt.Errorf("keyring: %w", err)
	}
	return nil
}

// Options configures a Store. ConfigDir is the profile config directory.
type Options struct {
	ConfigDir      string
	Profile        string
	Keyring        Keyring
	Rand           io.Reader
	BackendPresent func() bool // test override for the D-Bus probe
}

// Store reads and creates key material. It is safe for concurrent use.
type Store struct {
	dir, profile string
	kr           Keyring
	rand         io.Reader
	backend      func() bool
	mu           sync.Mutex
}

// New validates o and returns a Store. It performs no I/O.
func New(o Options) (*Store, error) {
	if o.ConfigDir == "" {
		return nil, fmt.Errorf("secret: empty config dir: %w", ErrKeyFile)
	}
	kr, rnd, be := o.Keyring, o.Rand, o.BackendPresent
	if kr == nil {
		kr = osKeyring{}
	}
	if rnd == nil {
		rnd = rand.Reader
	}
	if be == nil {
		be = backendPresent
	}
	return &Store{dir: o.ConfigDir, profile: o.Profile, kr: kr, rand: rnd, backend: be}, nil
}

func keyID(key []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("damnbot/key-id/v1"))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

func validID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16
}

func (s *Store) randomKey() ([]byte, error) {
	k := make([]byte, KeySize)
	if _, err := io.ReadFull(s.rand, k); err != nil {
		return nil, fmt.Errorf("secret: random key: %w", err)
	}
	return k, nil
}

func (s *Store) account(id string) string {
	a := atRestAccount
	if s.profile != "" {
		a += ":" + s.profile
	}
	if id != "" {
		a += ":" + id
	}
	return a
}

func (s *Store) keyringCall(ctx context.Context, fn func() (string, error)) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, keyringWait)
	defer cancel()
	if err := cctx.Err(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
	}
	// go-keyring cannot be cancelled; a call that outlives the deadline leaves
	// one goroutine blocked in the OS library. At most one such goroutine can
	// exist per timed-out call.
	ch := make(chan error, 1)
	var v string
	go func() { var err error; v, err = fn(); ch <- err }()
	select {
	case err := <-ch:
		return v, err
	case <-cctx.Done():
		return "", fmt.Errorf("%w: %w", ErrKeyUnavailable, cctx.Err())
	}
}

func (s *Store) keyringProbe(ctx context.Context) (bool, error) {
	if !s.backend() {
		return false, nil
	}
	_, err := s.keyringCall(ctx, func() (string, error) { return s.kr.Get(service, s.account("probe")) })
	switch {
	case err == nil, errors.Is(err, keyring.ErrNotFound):
		return true, nil
	case errors.Is(err, keyring.ErrUnsupportedPlatform):
		return false, nil
	default:
		if backendAbsent(err) {
			return false, nil
		}
		return false, fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
	}
}

func (s *Store) keyringGet(ctx context.Context, account string) (string, error) {
	v, err := s.keyringCall(ctx, func() (string, error) { return s.kr.Get(service, account) })
	if err == nil {
		return v, nil
	}
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrKeyMissing
	}
	return "", fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
}

func (s *Store) keyringSet(ctx context.Context, account, value string) error {
	_, err := s.keyringCall(ctx, func() (string, error) { return "", s.kr.Set(service, account, value) })
	if err != nil {
		return fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
	}
	return nil
}

func (s *Store) keyringDelete(ctx context.Context, account string) error {
	_, err := s.keyringCall(ctx, func() (string, error) { return "", s.kr.Delete(service, account) })
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
}

func (s *Store) openRoot() (*os.Root, error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, fmt.Errorf("secret: create config dir: %w", err)
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return nil, fmt.Errorf("secret: open config dir: %w", err)
	}
	d, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("secret: open config dir: %w", errors.Join(err, root.Close()))
	}
	fi, serr := d.Stat()
	cerr := d.Close()
	if err := errors.Join(serr, cerr); err != nil {
		return nil, fmt.Errorf("secret: stat config dir: %w", errors.Join(err, root.Close()))
	}
	if err := checkDir(fi); err != nil {
		return nil, errors.Join(err, root.Close())
	}
	return root, nil
}

func checkRegular(fi fs.FileInfo) error {
	if fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("secret: key path is a symlink: %w", ErrKeyFile)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("secret: key path is not a regular file: %w", ErrKeyFile)
	}
	return nil
}

func (s *Store) readSmall(name string) (b []byte, err error) {
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	f, err := openNoFollow(root, name)
	if err != nil {
		return nil, fmt.Errorf("secret: open %s: %w", name, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("secret: stat %s: %w", name, err)
	}
	if perr := checkFile(fi); perr != nil {
		return nil, perr
	}
	b, err = io.ReadAll(io.LimitReader(f, 8<<10))
	if err != nil {
		return nil, fmt.Errorf("secret: read %s: %w", name, err)
	}
	if len(b) > 8<<10 {
		return nil, fmt.Errorf("secret: %s is too large: %w", name, ErrKeyFile)
	}
	return b, nil
}

func (s *Store) writeTemp(root *os.Root, name string, data []byte) (string, error) {
	suffix, err := s.randomKey()
	if err != nil {
		return "", err
	}
	tmp := "." + name + ".tmp-" + hex.EncodeToString(suffix[:8])
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return "", fmt.Errorf("secret: create %s: %w", tmp, err)
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		return "", fmt.Errorf("secret: write %s: %w", tmp, errors.Join(err, removeIfExists(root, tmp)))
	}
	return tmp, nil
}

func (s *Store) writeAtomic(name string, data []byte) (err error) {
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	tmp, err := s.writeTemp(root, name, data)
	if err != nil {
		return err
	}
	if rerr := root.Rename(tmp, name); rerr != nil {
		return fmt.Errorf("secret: publish %s: %w", name, errors.Join(rerr, removeIfExists(root, tmp)))
	}
	return syncRootDir(root)
}

func (s *Store) createExclusive(name string, data []byte) (err error) {
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	tmp, err := s.writeTemp(root, name, data)
	if err != nil {
		return err
	}
	if lerr := root.Link(tmp, name); lerr != nil {
		lerr = errors.Join(lerr, removeIfExists(root, tmp))
		if errors.Is(lerr, fs.ErrExist) {
			return fs.ErrExist
		}
		return fmt.Errorf("secret: publish %s: %w", name, lerr)
	}
	return errors.Join(removeIfExists(root, tmp), syncRootDir(root))
}

func (s *Store) remove(name string) (err error) {
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	return errors.Join(removeIfExists(root, name), syncRootDir(root))
}

func removeIfExists(root *os.Root, name string) error {
	if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("secret: remove %s: %w", name, err)
	}
	return nil
}
