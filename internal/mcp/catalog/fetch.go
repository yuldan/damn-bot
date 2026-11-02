package catalog

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MaxManifestBytes bounds one fetched manifest (16 MiB).
const MaxManifestBytes = 16 << 20

// Doer is the subset of netx.Client used here. The caller supplies a client
// with the same-host redirect policy (WithSameHostRedirects), so a manifest
// cannot be fetched from a host different from the configured URL.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Fetch downloads one manifest and verifies it before returning. Only a 2xx
// response is read; a body over MaxManifestBytes fails closed.
func Fetch(ctx context.Context, client Doer, url string, pub ed25519.PublicKey, lastSerial int64) (Manifest, error) {
	if client == nil || url == "" {
		return Manifest{}, fmt.Errorf("%w: fetch needs a client and URL", ErrMalformed)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: request: %w", ErrMalformed, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return Manifest{}, fmt.Errorf("catalog: fetch: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			return
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Manifest{}, fmt.Errorf("catalog: fetch status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxManifestBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("catalog: read manifest: %w", err)
	}
	if len(raw) > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("%w: manifest exceeds %d bytes", ErrMalformed, MaxManifestBytes)
	}
	return Verify(raw, pub, lastSerial)
}

// FileSeqStore persists the last accepted serial per catalog in one directory
// (0600 files, atomic rename). It is the cross-process anti-rollback state of
// the CLI; a corrupt or unreadable file fails closed.
type FileSeqStore struct{ Dir string }

func (s FileSeqStore) path(kind Kind, name string) (string, error) {
	if s.Dir == "" || kind == "" || name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return "", fmt.Errorf("%w: invalid serial key", ErrMalformed)
	}
	return filepath.Join(s.Dir, string(kind)+"-"+name+".seq"), nil
}

// Last reads the stored serial; missing means 0 (first use).
func (s FileSeqStore) Last(kind Kind, name string) (int64, error) {
	path, err := s.path(kind, name)
	if err != nil {
		return 0, err
	}
	root, rerr := os.OpenRoot(filepath.Dir(path))
	if rerr != nil {
		if errors.Is(rerr, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("catalog: serial dir: %w", rerr)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			return
		}
	}()
	f, ferr := root.Open(filepath.Base(path))
	if errors.Is(ferr, os.ErrNotExist) {
		return 0, nil
	}
	if ferr != nil {
		return 0, fmt.Errorf("catalog: read serial: %w", ferr)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			return
		}
	}()
	raw, err := io.ReadAll(io.LimitReader(f, 128))
	if err != nil {
		return 0, fmt.Errorf("catalog: read serial: %w", err)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: corrupt serial file", ErrMalformed)
	}
	return n, nil
}

// Save atomically replaces the serial. The file is created 0600 and renamed
// only after a successful write.
func (s FileSeqStore) Save(kind Kind, name string, serial int64) error {
	path, err := s.path(kind, name)
	if err != nil {
		return err
	}
	if merr := os.MkdirAll(s.Dir, 0o700); merr != nil {
		return fmt.Errorf("catalog: serial dir: %w", merr)
	}
	tmp, err := os.CreateTemp(s.Dir, ".seq-*")
	if err != nil {
		return fmt.Errorf("catalog: serial temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		if rerr := os.Remove(tmpName); rerr != nil {
			return
		}
	}
	if err := tmp.Chmod(0o600); err != nil {
		if cerr := tmp.Close(); cerr != nil {
			cleanup()
			return fmt.Errorf("catalog: serial chmod: %w", err)
		}
		cleanup()
		return fmt.Errorf("catalog: serial chmod: %w", err)
	}
	if _, err := fmt.Fprintf(tmp, "%d\n", serial); err != nil {
		if cerr := tmp.Close(); cerr != nil {
			cleanup()
			return fmt.Errorf("catalog: serial write: %w", err)
		}
		cleanup()
		return fmt.Errorf("catalog: serial write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("catalog: serial close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("catalog: serial rename: %w", err)
	}
	return nil
}
