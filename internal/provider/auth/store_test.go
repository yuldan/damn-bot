package auth

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/secret"
	"github.com/zalando/go-keyring"
)

type fakeKeyring struct {
	mu   sync.Mutex
	m    map[string]string
	fail bool
}

func (k *fakeKeyring) Get(service, user string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fail {
		return "", errors.New("keychain down")
	}
	v, ok := k.m[service+"/"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (k *fakeKeyring) Set(service, user, value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fail {
		return errors.New("keychain down")
	}
	k.m[service+"/"+user] = value
	return nil
}

func (k *fakeKeyring) Delete(service, user string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fail {
		return errors.New("keychain down")
	}
	delete(k.m, service+"/"+user)
	return nil
}

func keychainStore(t *testing.T, kr *fakeKeyring, dir string) *Store {
	t.Helper()
	cfgDir := t.TempDir()
	if err := os.Chmod(cfgDir, 0o700); err != nil { // #nosec G302 -- test dir needs 0700
		t.Fatal(err)
	}
	sec, err := secret.New(secret.Options{ConfigDir: cfgDir, Keyring: kr, BackendPresent: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	st, err := NewStore(sec, dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func sampleToken(name string) Token {
	return Token{
		Provider: name, AccessToken: "access-SECRET", RefreshToken: "refresh-SECRET",
		Issuer: "https://issuer.example", ClientID: "client-1",
		Expiry: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
	}
}

// TestStoreFileRoundTrip: headless storage is one 0600 file in a 0700 dir.
func TestStoreFileRoundTrip(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "auth")
	st, nerr := NewStore(nil, dir)
	if nerr != nil {
		t.Fatal(nerr)
	}
	ctx := context.Background()
	alpha := sampleToken("alpha")
	if err := st.Save(ctx, &alpha); err != nil {
		t.Fatal(err)
	}
	di, derr := os.Stat(dir)
	if derr != nil || (runtime.GOOS != "windows" && di.Mode().Perm() != 0o700) {
		t.Fatalf("dir mode = %v, %v", di.Mode().Perm(), derr)
	}
	fi, ferr := os.Stat(filepath.Join(dir, "alpha.json"))
	if ferr != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("file mode = %v, %v", fi.Mode().Perm(), ferr)
	}
	got, err := st.Load(ctx, "alpha")
	if err != nil || got.AccessToken != "access-SECRET" {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	if err := st.Delete(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(ctx, "alpha"); !errors.Is(err, ErrTokenMissing) {
		t.Fatalf("Load after delete = %v", err)
	}
	if err := st.Delete(ctx, "alpha"); !errors.Is(err, ErrTokenMissing) {
		t.Fatalf("second delete = %v", err)
	}
}

// TestStoreKeychainRoundTrip: a working keychain keeps the token off disk.
func TestStoreKeychainRoundTrip(t *testing.T) {
	t.Parallel()
	kr := &fakeKeyring{m: map[string]string{}}
	dir := filepath.Join(t.TempDir(), "auth")
	st := keychainStore(t, kr, dir)
	ctx := context.Background()
	beta := sampleToken("beta")
	if err := st.Save(ctx, &beta); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "beta.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("keychain save wrote a file: %v", err)
	}
	if _, err := st.Load(ctx, "beta"); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(ctx, "beta"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(ctx, "beta"); !errors.Is(err, ErrTokenMissing) {
		t.Fatalf("Load after delete = %v", err)
	}
}

// TestStoreKeychainDownFallsBack: a down keychain degrades to the file.
func TestStoreKeychainDownFallsBack(t *testing.T) {
	t.Parallel()
	kr := &fakeKeyring{m: map[string]string{}, fail: true}
	dir := filepath.Join(t.TempDir(), "auth")
	st := keychainStore(t, kr, dir)
	ctx := context.Background()
	gamma := sampleToken("gamma")
	if err := st.Save(ctx, &gamma); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gamma.json")); err != nil {
		t.Fatalf("fallback file missing: %v", err)
	}
	if _, err := st.Load(ctx, "gamma"); err != nil {
		t.Fatal(err)
	}
}

// TestStoreRejectsWideModesAndBadNames is the negative control.
func TestStoreRejectsWideModesAndBadNames(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	dir := filepath.Join(t.TempDir(), "auth")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { // #nosec G302 -- the wide mode is the negative control
		t.Fatal(err)
	}
	st, nerr := NewStore(nil, dir)
	if nerr != nil {
		t.Fatal(nerr)
	}
	delta := sampleToken("delta")
	if err := st.Save(context.Background(), &delta); !errors.Is(err, secret.ErrPermissions) {
		t.Fatalf("wide dir save = %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- test dir needs 0700
		t.Fatal(err)
	}
	if err := st.Save(context.Background(), &delta); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "delta.json"), 0o644); err != nil { // #nosec G302 -- the wide mode is the negative control
		t.Fatal(err)
	}
	if _, err := st.Load(context.Background(), "delta"); !errors.Is(err, secret.ErrPermissions) {
		t.Fatalf("wide file load = %v", err)
	}
	for _, name := range []string{"../evil", "Alpha", "a/b", ""} {
		if err := ValidateProvider(name); err == nil {
			t.Fatalf("provider %q accepted", name)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(context.Background(), "broken"); err == nil {
		t.Fatal("corrupt token accepted")
	}
}

// TestTokenExpired pins the refresh window.
func TestTokenExpired(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	tok := Token{Expiry: now.Add(2 * time.Minute)}
	if tok.Expired(now) {
		t.Fatal("fresh token reported expired")
	}
	if !tok.Expired(now.Add(61 * time.Second)) {
		t.Fatal("token inside the skew reported fresh")
	}
	if (&Token{}).Expired(now) {
		t.Fatal("no-expiry token reported expired")
	}
}
