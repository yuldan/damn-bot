package secret

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

// KEK is the at-rest key-encryption key. Source and ID record where it lives;
// callers persist both so a later open never guesses a different source.
type KEK struct {
	Key    []byte
	Source Source
	ID     string
}

// String redacts key material.
func (k KEK) String() string {
	return fmt.Sprintf("secret.KEK{source:%s id:%s key:[redacted]}", k.Source, k.ID)
}

// GoString redacts key material.
func (k KEK) GoString() string { return k.String() }

// Format redacts key material for every fmt verb.
func (k KEK) Format(s fmt.State, _ rune) {
	if _, err := fmt.Fprint(s, k.String()); err != nil {
		return
	}
}

// Zero wipes the key buffer.
func (k *KEK) Zero() { clear(k.Key); k.Key = nil }

// AtRest loads the KEK from exactly the recorded source and id. It never
// consults the other source: a planted file cannot replace a keychain key and
// a keychain outage cannot fall back to a file.
func (s *Store) AtRest(ctx context.Context, src Source, id string) (KEK, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.atRest(ctx, src, id)
}

func (s *Store) atRest(ctx context.Context, src Source, id string) (KEK, error) {
	if !validID(id) {
		return KEK{}, fmt.Errorf("%w: id %q", ErrKeyFile, id)
	}
	switch src {
	case SourceKeychain:
		v, err := s.keyringGet(ctx, s.account(id))
		if err != nil {
			return KEK{}, err
		}
		key, err := base64.StdEncoding.DecodeString(v)
		if err != nil || len(key) != KeySize || keyID(key) != id {
			return KEK{}, fmt.Errorf("%w: keychain at-rest key", ErrKeyFile)
		}
		return KEK{Key: key, Source: src, ID: id}, nil
	case SourceFile:
		b, err := s.readSmall(keyName(id))
		if errors.Is(err, fs.ErrNotExist) {
			return KEK{}, ErrKeyMissing
		}
		if err != nil {
			return KEK{}, err
		}
		if len(b) != KeySize || keyID(b) != id {
			clear(b)
			return KEK{}, fmt.Errorf("%w: at-rest key %s", ErrKeyFile, id)
		}
		return KEK{Key: b, Source: src, ID: id}, nil
	default:
		return KEK{}, fmt.Errorf("%w: %q", ErrSource, src)
	}
}

// EnsureAtRest loads the recorded KEK, or provisions one exactly once. A
// recorded source is never replaced: an outage is ErrKeyUnavailable.
func (s *Store) EnsureAtRest(ctx context.Context) (KEK, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, id, err := s.loadMarker()
	if err == nil {
		return s.atRest(ctx, src, id)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return KEK{}, err
	}
	return s.provision(ctx)
}

func (s *Store) provision(ctx context.Context) (KEK, error) {
	useKeychain, err := s.keyringProbe(ctx)
	if err != nil {
		return KEK{}, err
	}
	key, err := s.randomKey()
	if err != nil {
		return KEK{}, err
	}
	k := KEK{Key: key, Source: SourceFile, ID: keyID(key)}
	if useKeychain {
		k.Source = SourceKeychain
	}
	if err := s.storeKey(ctx, k); err != nil {
		k.Zero()
		return KEK{}, err
	}
	if err := s.createExclusive(atRestMeta, marker(k)); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			k.Zero()
			return KEK{}, err
		}
		src, id, merr := s.loadMarker()
		cerr := s.deleteKey(ctx, k)
		k.Zero()
		if merr != nil {
			return KEK{}, errors.Join(merr, cerr)
		}
		if cerr != nil {
			return KEK{}, cerr
		}
		return s.atRest(ctx, src, id)
	}
	return k, nil
}

// StageAtRest stores a new KEK without changing the marker; the old key stays
// active until CommitAtRest.
func (s *Store) StageAtRest(ctx context.Context, old KEK) (KEK, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	loaded, err := s.atRest(ctx, old.Source, old.ID)
	if err != nil {
		return KEK{}, err
	}
	loaded.Zero()
	key, err := s.randomKey()
	if err != nil {
		return KEK{}, err
	}
	next := KEK{Key: key, Source: old.Source, ID: keyID(key)}
	if err := s.storeKey(ctx, next); err != nil {
		next.Zero()
		return KEK{}, err
	}
	return next, nil
}

// CommitAtRest points the marker at next after the DEK has been rewrapped.
func (s *Store) CommitAtRest(ctx context.Context, next KEK) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	loaded, err := s.atRest(ctx, next.Source, next.ID)
	if err != nil {
		return err
	}
	loaded.Zero()
	return s.writeAtomic(atRestMeta, marker(next))
}

// RetireAtRest deletes old after commit and rewrap; it refuses while the
// marker still names old.
func (s *Store) RetireAtRest(ctx context.Context, old KEK) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, id, err := s.loadMarker()
	if err != nil {
		return err
	}
	if id == old.ID {
		return fmt.Errorf("%w: marker still names the old key", ErrKeySource)
	}
	return s.deleteKey(ctx, old)
}

func (s *Store) storeKey(ctx context.Context, k KEK) error {
	switch k.Source {
	case SourceKeychain:
		return s.keyringSet(ctx, s.account(k.ID), base64.StdEncoding.EncodeToString(k.Key))
	case SourceFile:
		return s.createExclusive(keyName(k.ID), k.Key)
	default:
		return fmt.Errorf("%w: %q", ErrSource, k.Source)
	}
}

func (s *Store) deleteKey(ctx context.Context, k KEK) error {
	switch k.Source {
	case SourceKeychain:
		return s.keyringDelete(ctx, s.account(k.ID))
	case SourceFile:
		if !validID(k.ID) {
			return fmt.Errorf("%w: id", ErrKeyFile)
		}
		return s.remove(keyName(k.ID))
	default:
		return fmt.Errorf("%w: %q", ErrSource, k.Source)
	}
}
func keyName(id string) string { return atRestPrefix + id + atRestSuffix }
func marker(k KEK) []byte      { return []byte("v1 " + string(k.Source) + " " + k.ID + "\n") }
func (s *Store) loadMarker() (Source, string, error) {
	b, err := s.readSmall(atRestMeta)
	if err != nil {
		return "", "", err
	}
	defer clear(b)
	f := strings.Fields(string(b))
	if len(f) != 3 || f[0] != "v1" || !validID(f[2]) {
		return "", "", fmt.Errorf("%w: at-rest marker", ErrKeyFile)
	}
	src := Source(f[1])
	if src != SourceFile && src != SourceKeychain {
		return "", "", fmt.Errorf("%w: marker source %q", ErrSource, src)
	}
	return src, f[2], nil
}
