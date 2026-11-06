package gwpeer

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// LoadOrCreateKey reads the local Ed25519 seed at path, generating it with
// mode 0600 on first use. The seed is 32 bytes; a malformed file is rejected
// rather than replaced, so an operator notice is never masked by a new key.
func LoadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("gwpeer: create key directory: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("gwpeer: open key directory: %w", err)
	}
	key, loadErr := readOrCreateSeed(root, filepath.Base(path))
	closeErr := root.Close()
	if loadErr != nil || closeErr != nil {
		return nil, errors.Join(loadErr, closeErr)
	}
	return key, nil
}

// readOrCreateSeed reads the 32-byte seed and creates it with mode 0600 when
// absent. A malformed file is rejected rather than replaced.
func readOrCreateSeed(root *os.Root, name string) (ed25519.PrivateKey, error) {
	seed, err := root.ReadFile(name)
	switch {
	case err == nil:
		if len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("%w: key file has %d bytes, want %d", ErrSignature, len(seed), ed25519.SeedSize)
		}
		return ed25519.NewKeyFromSeed(seed), nil
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, fmt.Errorf("gwpeer: read key: %w", err)
	}
	seed = make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("gwpeer: generate key: %w", err)
	}
	if err := root.WriteFile(name, seed, 0o600); err != nil {
		return nil, fmt.Errorf("gwpeer: write key: %w", err)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
