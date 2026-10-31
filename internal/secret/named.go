package secret

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strings"
)

const secretsIndex = "secrets.index"

// NamedEntry is one index row: a name and a value fingerprint, never a value.
type NamedEntry struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
}

// Set stores a new value in the OS keychain, then records its fingerprint.
func (s *Store) Set(ctx context.Context, name, value string) error {
	return s.put(ctx, name, value, false)
}

// Rotate replaces an existing value and fingerprint.
func (s *Store) Rotate(ctx context.Context, name, value string) error {
	return s.put(ctx, name, value, true)
}

func (s *Store) put(ctx context.Context, name, value string, rotate bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, err := s.readSecrets()
	if err != nil {
		return err
	}
	if _, exists := idx[name]; exists && !rotate {
		return fs.ErrExist
	}
	if _, exists := idx[name]; !exists && rotate {
		return ErrKeyMissing
	}
	acct := s.account("named:" + name)
	if err := s.keyringSet(ctx, acct, value); err != nil {
		return err
	}
	idx[name] = fingerprint(value)
	return s.writeSecrets(idx)
}

// Get returns one value for in-process callers; the CLI never prints it.
func (s *Store) Get(ctx context.Context, name string) (string, error) {
	return s.keyringGet(ctx, s.account("named:"+name))
}

// Delete removes a named value from the keychain and its index row. A missing
// name is [ErrKeyMissing] so logout cannot report success for a value that was
// never removed.
func (s *Store) Delete(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, err := s.readSecrets()
	if err != nil {
		return err
	}
	if _, exists := idx[name]; !exists {
		return ErrKeyMissing
	}
	if err := s.keyringDelete(ctx, s.account("named:"+name)); err != nil {
		return err
	}
	delete(idx, name)
	return s.writeSecrets(idx)
}

// List returns the name+fingerprint index, sorted by name.
func (s *Store) List() ([]NamedEntry, error) {
	idx, err := s.readSecrets()
	if err != nil {
		return nil, err
	}
	out := make([]NamedEntry, 0, len(idx))
	for _, name := range slices.Sorted(maps.Keys(idx)) {
		out = append(out, NamedEntry{name, idx[name]})
	}
	return out, nil
}

func (s *Store) readSecrets() (map[string]string, error) {
	b, err := s.readSmall(secretsIndex)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	out := map[string]string{}
	for line := range strings.SplitSeq(string(b), "\n") {
		if name, fp, ok := strings.Cut(line, "\t"); ok {
			out[name] = fp
		}
	}
	return out, nil
}

func (s *Store) writeSecrets(idx map[string]string) error {
	var b strings.Builder
	for _, name := range slices.Sorted(maps.Keys(idx)) {
		b.WriteString(name + "\t" + idx[name] + "\n")
	}
	return s.writeAtomic(secretsIndex, []byte(b.String()))
}

func fingerprint(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
