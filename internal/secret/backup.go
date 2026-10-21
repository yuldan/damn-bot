package secret

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"

	"golang.org/x/crypto/curve25519"
)

const backupFile = "backup.keys"

// BackupKey is an X25519 key pair used as the recipient of encrypted backup
// archives. ID is derived from the public key.
type BackupKey struct {
	ID      string
	Private []byte
	Public  []byte
}

// String redacts the private key.
func (k BackupKey) String() string {
	return fmt.Sprintf("secret.BackupKey{id:%s key:[redacted]}", k.ID)
}

// GoString redacts the private key.
func (k BackupKey) GoString() string { return k.String() }

// Format redacts the private key for every fmt verb.
func (k BackupKey) Format(s fmt.State, _ rune) {
	if _, err := fmt.Fprint(s, k.String()); err != nil {
		return
	}
}

// Zero wipes the private key.
func (k *BackupKey) Zero() { clear(k.Private); k.Private = nil }

type backupMeta struct {
	Version int               `json:"v"`
	Backend Source            `json:"backend"`
	Current string            `json:"current"`
	Keys    map[string]string `json:"keys,omitempty"` // private keys, file backend only
}

// Backup returns the current backup key, creating the first key when none
// exists. The key is created in the keychain when one is reachable, otherwise
// in a 0600 file.
func (s *Store) Backup(ctx context.Context) (BackupKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, err := s.loadBackupMeta()
	if err == nil {
		return s.backupFromMeta(ctx, meta, meta.Current)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return BackupKey{}, err
	}
	key, err := s.newBackupKey()
	if err != nil {
		return BackupKey{}, err
	}
	meta = &backupMeta{Version: 1, Backend: SourceFile, Current: key.ID, Keys: map[string]string{key.ID: base64.StdEncoding.EncodeToString(key.Private)}}
	useKeychain, err := s.keyringProbe(ctx)
	if err != nil {
		return BackupKey{}, err
	}
	if useKeychain {
		if serr := s.keyringSet(ctx, s.backupAccount(key.ID), base64.StdEncoding.EncodeToString(key.Private)); serr != nil {
			return BackupKey{}, serr
		}
		meta = &backupMeta{Version: 1, Backend: SourceKeychain, Current: key.ID}
	}
	data, err := encodeJSON(meta)
	if err != nil {
		return BackupKey{}, err
	}
	if err := s.createExclusive(backupFile, data); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return s.backupCurrent(ctx) // another process won the first-use race
		}
		return BackupKey{}, err
	}
	return key, nil
}

// BackupByID loads one specific key. It is used to open an archive created
// before a rotation.
func (s *Store) BackupByID(ctx context.Context, id string) (BackupKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, err := s.loadBackupMeta()
	if err != nil {
		return BackupKey{}, err
	}
	return s.backupFromMeta(ctx, meta, id)
}

// BackupAvailable reports whether id can be loaded right now. A missing key is
// (false, nil); an unreachable keychain is an error.
func (s *Store) BackupAvailable(ctx context.Context, id string) (bool, error) {
	k, err := s.BackupByID(ctx, id)
	if err == nil {
		return k.ID == id, nil
	}
	if errors.Is(err, ErrKeyMissing) {
		return false, nil
	}
	return false, err
}

// RotateBackup creates a new current key and keeps every old key available for
// reading archives that name its id.
func (s *Store) RotateBackup(ctx context.Context) (BackupKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, err := s.loadBackupMeta()
	if err != nil {
		return BackupKey{}, err
	}
	key, err := s.newBackupKey()
	if err != nil {
		return BackupKey{}, err
	}
	priv := base64.StdEncoding.EncodeToString(key.Private)
	if meta.Backend == SourceKeychain {
		if serr := s.keyringSet(ctx, s.backupAccount(key.ID), priv); serr != nil {
			return BackupKey{}, serr
		}
	} else {
		if meta.Keys == nil {
			meta.Keys = map[string]string{}
		}
		meta.Keys[key.ID] = priv
	}
	meta.Current = key.ID
	data, err := encodeJSON(meta)
	if err != nil {
		return BackupKey{}, err
	}
	if err := s.writeAtomic(backupFile, data); err != nil {
		return BackupKey{}, err
	}
	return key, nil
}

func (s *Store) backupCurrent(ctx context.Context) (BackupKey, error) {
	meta, err := s.loadBackupMeta()
	if err != nil {
		return BackupKey{}, err
	}
	return s.backupFromMeta(ctx, meta, meta.Current)
}

func (s *Store) backupFromMeta(ctx context.Context, meta *backupMeta, id string) (BackupKey, error) {
	if meta.Version != 1 || id == "" {
		return BackupKey{}, fmt.Errorf("%w: backup metadata", ErrKeyFile)
	}
	var enc string
	switch meta.Backend {
	case SourceFile:
		enc = meta.Keys[id]
	case SourceKeychain:
		v, err := s.keyringGet(ctx, s.backupAccount(id))
		if err != nil {
			return BackupKey{}, err
		}
		enc = v
	default:
		return BackupKey{}, fmt.Errorf("%w: %q", ErrSource, meta.Backend)
	}
	if enc == "" {
		return BackupKey{}, fmt.Errorf("%w: backup key %s", ErrKeyMissing, id)
	}
	priv, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(priv) != KeySize {
		return BackupKey{}, fmt.Errorf("%w: backup key %s", ErrKeyFile, id)
	}
	return backupKey(priv)
}

func (s *Store) backupAccount(id string) string {
	a := "backup"
	if s.profile != "" {
		a += ":" + s.profile
	}
	return a + ":" + id
}

func (s *Store) newBackupKey() (BackupKey, error) {
	priv, err := s.randomKey()
	if err != nil {
		return BackupKey{}, err
	}
	return backupKey(priv)
}

func backupKey(priv []byte) (BackupKey, error) {
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return BackupKey{}, fmt.Errorf("secret: derive backup public key: %w", err)
	}
	id := keyID(pub)
	if id == "" {
		return BackupKey{}, fmt.Errorf("%w: empty backup id", ErrKeyFile)
	}
	return BackupKey{ID: id, Private: priv, Public: pub}, nil
}

func (s *Store) loadBackupMeta() (*backupMeta, error) {
	b, err := s.readKeyFileNamed(backupFile)
	if err != nil {
		return nil, err
	}
	var meta backupMeta
	if err := decodeJSON(b, &meta); err != nil || meta.Version != 1 {
		return nil, fmt.Errorf("%w: backup metadata", ErrKeyFile)
	}
	return &meta, nil
}
