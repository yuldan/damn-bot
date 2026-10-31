package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/secret"
)

// Token is one stored OAuth credential. AccessToken and RefreshToken are
// secret; the remaining fields are metadata safe for `setup auth status`.
type Token struct {
	Provider           string    `json:"provider"`
	AccessToken        string    `json:"access_token,omitempty"`
	RefreshToken       string    `json:"refresh_token,omitempty"`
	Scope              string    `json:"scope,omitempty"`
	Expiry             time.Time `json:"expiry,omitzero"`
	Issuer             string    `json:"issuer,omitempty"`
	ClientID           string    `json:"client_id,omitempty"`
	ClientSecret       string    `json:"client_secret,omitempty"`
	TokenEndpoint      string    `json:"token_endpoint,omitempty"`
	RevocationEndpoint string    `json:"revocation_endpoint,omitempty"`
}

// Expired reports whether the access token is past (or within tokenSkew of)
// its expiry; a token without expiry never expires.
func (t *Token) Expired(now time.Time) bool {
	return !t.Expiry.IsZero() && !now.Add(tokenSkew).Before(t.Expiry)
}

// ValidateProvider rejects a name that could escape the token directory.
func ValidateProvider(name string) error {
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`).MatchString(name) {
		return fmt.Errorf("%w: invalid provider name %q", ErrCredential, name)
	}
	return nil
}

// Store keeps tokens in the OS keychain when usable and otherwise in one
// 0600 JSON file per provider below a 0700 directory. Safe for concurrent use.
type Store struct {
	dir string
	sec *secret.Store
	mu  sync.Mutex
}

// NewStore validates the fallback directory and the optional keychain store.
func NewStore(sec *secret.Store, dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("%w: empty token directory", ErrCredential)
	}
	return &Store{dir: dir, sec: sec}, nil
}

func tokenName(provider string) string { return "oauth:" + provider }

// Load reads the keychain first, then the file.
func (s *Store) Load(ctx context.Context, provider string) (Token, error) {
	if err := ValidateProvider(provider); err != nil {
		return Token{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sec != nil {
		raw, kerr := s.sec.Get(ctx, tokenName(provider))
		switch {
		case kerr == nil:
			return decodeToken(provider, []byte(raw))
		case !errors.Is(kerr, secret.ErrKeyMissing) && !errors.Is(kerr, secret.ErrKeyUnavailable):
			return Token{}, fmt.Errorf("auth: read keychain token: %w", kerr)
		}
	}
	path := filepath.Join(s.dir, provider+".json")
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Token{}, fmt.Errorf("%w: %s", ErrTokenMissing, provider)
	}
	if err != nil {
		return Token{}, fmt.Errorf("auth: stat token: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return Token{}, fmt.Errorf("%w: token path is not a regular file", secret.ErrPermissions)
	}
	if err := checkModes(s.dir, fi); err != nil {
		return Token{}, err
	}
	body, rerr := os.ReadFile(path) // #nosec G304 -- path is a validated provider name under the owned token dir
	if rerr != nil {
		return Token{}, fmt.Errorf("auth: read token: %w", rerr)
	}
	return decodeToken(provider, body)
}

// Save writes the keychain entry, falling back to a 0600 file.
func (s *Store) Save(ctx context.Context, t *Token) error {
	if err := ValidateProvider(t.Provider); err != nil {
		return err
	}
	raw, err := json.Marshal(t) //nolint:gosec // G117: the token store must serialize the credential it owns
	if err != nil {
		return fmt.Errorf("auth: encode token: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sec != nil {
		kerr := s.sec.Rotate(ctx, tokenName(t.Provider), string(raw))
		if errors.Is(kerr, secret.ErrKeyMissing) {
			kerr = s.sec.Set(ctx, tokenName(t.Provider), string(raw))
		}
		switch {
		case kerr == nil:
			if rerr := os.Remove(filepath.Join(s.dir, t.Provider+".json")); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				return fmt.Errorf("auth: remove stale token file: %w", rerr)
			}
			return nil
		case !errors.Is(kerr, secret.ErrKeyUnavailable):
			return fmt.Errorf("auth: write keychain token: %w", kerr)
		}
	}
	f, err := createTokenFile(s.dir)
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }() //nolint:errcheck // temp cleanup; rename already moved it on success
	if _, err := f.Write(raw); err != nil {
		return errors.Join(fmt.Errorf("auth: write token file: %w", err), f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(fmt.Errorf("auth: sync token file: %w", err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("auth: close token file: %w", err)
	}
	if err := os.Rename(name, filepath.Join(s.dir, t.Provider+".json")); err != nil {
		return fmt.Errorf("auth: commit token file: %w", err)
	}
	return nil
}

// createTokenFile returns a 0600 temp file in a 0700 directory.
func createTokenFile(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("auth: create token dir: %w", err)
	}
	if err := checkModes(dir, nil); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return nil, fmt.Errorf("auth: create token file: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		return nil, errors.Join(fmt.Errorf("auth: chmod token file: %w", err), f.Close())
	}
	return f, nil
}

// Delete removes the token from both backends; missing everywhere is
// [ErrTokenMissing].
func (s *Store) Delete(ctx context.Context, provider string) error {
	if err := ValidateProvider(provider); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	if s.sec != nil {
		err := s.sec.Delete(ctx, tokenName(provider))
		switch {
		case err == nil:
			found = true
		case !errors.Is(err, secret.ErrKeyMissing) && !errors.Is(err, secret.ErrKeyUnavailable):
			return fmt.Errorf("auth: delete keychain token: %w", err)
		}
	}
	err := os.Remove(filepath.Join(s.dir, provider+".json"))
	switch {
	case err == nil:
		found = true
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("auth: delete token file: %w", err)
	}
	if !found {
		return fmt.Errorf("%w: %s", ErrTokenMissing, provider)
	}
	return nil
}

func decodeToken(provider string, raw []byte) (Token, error) {
	var t Token
	if err := json.Unmarshal(raw, &t); err != nil {
		return Token{}, fmt.Errorf("auth: decode token: %w", err)
	}
	if t.Provider != provider {
		return Token{}, fmt.Errorf("%w: token provider mismatch", ErrCredential)
	}
	if t.AccessToken == "" && t.RefreshToken == "" {
		return Token{}, fmt.Errorf("%w: token has no credential", ErrCredential)
	}
	return t, nil
}

// checkModes fails closed on a directory or file mode wider than 0700/0600
// (POSIX only; Windows has no such bits).
func checkModes(dir string, file fs.FileInfo) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	di, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("auth: stat token dir: %w", err)
	}
	if di.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: token dir mode %04o", secret.ErrPermissions, di.Mode().Perm())
	}
	if file != nil && file.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: token file mode %04o", secret.ErrPermissions, file.Mode().Perm())
	}
	return nil
}
