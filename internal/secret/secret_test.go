package secret

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

type fakeKeyring struct {
	mu          sync.Mutex
	m           map[string]string
	down        bool
	unsupported bool
	getErr      error
	block       chan struct{}
}

func newFakeKeyring() *fakeKeyring { return &fakeKeyring{m: map[string]string{}} }

func (f *fakeKeyring) wait() {
	f.mu.Lock()
	ch := f.block
	f.mu.Unlock()
	if ch != nil {
		<-ch
	}
}

func (f *fakeKeyring) Get(service, user string) (string, error) {
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.getErr != nil:
		return "", f.getErr
	case f.unsupported:
		return "", keyring.ErrUnsupportedPlatform
	case f.down:
		return "", errors.New("dbus unavailable")
	}
	if v, ok := f.m[service+"/"+user]; ok {
		return v, nil
	}
	return "", keyring.ErrNotFound
}

func (f *fakeKeyring) Set(service, user, value string) error {
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return errors.New("dbus unavailable")
	}
	f.m[service+"/"+user] = value
	return nil
}

func (f *fakeKeyring) Delete(service, user string) error {
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return errors.New("dbus unavailable")
	}
	delete(f.m, service+"/"+user)
	return nil
}

func (f *fakeKeyring) setDown()                 { f.mu.Lock(); f.down = true; f.mu.Unlock() }
func (f *fakeKeyring) blockOn(ch chan struct{}) { f.mu.Lock(); f.block = ch; f.mu.Unlock() }

func tempStore(t *testing.T, kr Keyring) *Store {
	t.Helper()
	s, err := New(Options{ConfigDir: filepath.Join(t.TempDir(), "damnbot"), Keyring: kr, BackendPresent: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "at-rest.*.key"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestProvisionFile(t *testing.T) {
	kr := newFakeKeyring()
	kr.unsupported = true
	s := tempStore(t, kr)
	k, err := s.EnsureAtRest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if k.Source != SourceFile || len(k.Key) != KeySize || k.ID != keyID(k.Key) {
		t.Fatalf("unexpected key %+v", k)
	}
	if got := files(t, s.dir); len(got) != 1 {
		t.Fatalf("key files = %v", got)
	}
	for _, p := range []string{filepath.Join(s.dir, atRestMeta), keyPath(s.dir, k.ID)} {
		fi, serr := os.Stat(p)
		if serr != nil {
			t.Fatal(serr)
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %04o", p, fi.Mode().Perm())
		}
	}
	di, derr := os.Stat(s.dir)
	if derr != nil {
		t.Fatal(derr)
	}
	if runtime.GOOS != "windows" && di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %04o", di.Mode().Perm())
	}
	again, err := s.EnsureAtRest(context.Background())
	if err != nil || again.ID != k.ID {
		t.Fatalf("second Ensure = %+v, %v", again, err)
	}
}

func keyPath(dir, id string) string { return filepath.Join(dir, keyName(id)) }

func TestProvisionKeychain(t *testing.T) {
	kr := newFakeKeyring()
	s := tempStore(t, kr)
	k, err := s.EnsureAtRest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if k.Source != SourceKeychain {
		t.Fatalf("source = %s", k.Source)
	}
	if got := files(t, s.dir); len(got) != 0 {
		t.Fatalf("key material leaked to file: %v", got)
	}
}

func TestNoSilentFallback(t *testing.T) {
	kr := newFakeKeyring()
	s := tempStore(t, kr)
	k, err := s.EnsureAtRest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	kr.setDown()
	if _, err := s.EnsureAtRest(context.Background()); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("keychain down = %v, want ErrKeyUnavailable", err)
	}
	if got := files(t, s.dir); len(got) != 0 {
		t.Fatalf("fallback key created: %v", got)
	}
	if _, err := s.AtRest(context.Background(), k.Source, k.ID); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("AtRest = %v", err)
	}
}

func TestPlantedFileIgnored(t *testing.T) {
	kr := newFakeKeyring()
	s := tempStore(t, kr)
	k, err := s.EnsureAtRest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	attacker := make([]byte, KeySize)
	if _, rerr := rand.Read(attacker); rerr != nil {
		t.Fatal(rerr)
	}
	if werr := os.WriteFile(keyPath(s.dir, keyID(attacker)), attacker, 0o600); werr != nil {
		t.Fatal(werr)
	}
	got, err := s.EnsureAtRest(context.Background())
	if err != nil || got.ID != k.ID || hex.EncodeToString(got.Key) != hex.EncodeToString(k.Key) {
		t.Fatalf("planted file won: %+v, %v", got, err)
	}
	kr.setDown()
	if _, err := s.EnsureAtRest(context.Background()); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("keychain down used planted file: %v", err)
	}
}

func TestMarkerMalformed(t *testing.T) {
	s := tempStore(t, newFakeKeyring())
	if _, err := s.EnsureAtRest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, atRestMeta), []byte("junk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureAtRest(context.Background()); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("malformed marker = %v", err)
	}
}

func TestPermissionsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	kr := newFakeKeyring()
	kr.unsupported = true
	s := tempStore(t, kr)
	k, err := s.EnsureAtRest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath(s.dir, k.ID), 0o644); err != nil { //nolint:gosec // deliberately widen
		t.Fatal(err)
	}
	if _, err := s.AtRest(context.Background(), SourceFile, k.ID); !errors.Is(err, ErrPermissions) {
		t.Fatalf("wide file = %v, want ErrPermissions", err)
	}
	if err := os.Chmod(s.dir, 0o755); err != nil { //nolint:gosec // deliberately widen
		t.Fatal(err)
	}
	if _, err := s.EnsureAtRest(context.Background()); !errors.Is(err, ErrPermissions) {
		t.Fatalf("wide dir = %v, want ErrPermissions", err)
	}
}

func TestSymlinkRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges")
	}
	kr := newFakeKeyring()
	kr.unsupported = true
	s := tempStore(t, kr)
	k, err := s.EnsureAtRest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	path := keyPath(s.dir, k.ID)
	outside := filepath.Join(t.TempDir(), "outside.key")
	if err := os.WriteFile(outside, k.Key, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AtRest(context.Background(), SourceFile, k.ID); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("symlink = %v, want ErrKeyFile", err)
	}
}

func TestRedactionAndZero(t *testing.T) {
	s := tempStore(t, newFakeKeyring())
	k, err := s.EnsureAtRest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	hexKey := hex.EncodeToString(k.Key)
	for _, got := range []string{fmt.Sprintf("%v", k), fmt.Sprintf("%+v", k), k.String(), fmt.Sprintf("%#v", k)} {
		if strings.Contains(got, hexKey) || !strings.Contains(got, "[redacted]") {
			t.Fatalf("key leaked in %q", got)
		}
	}
	k.Zero()
	if len(k.Key) != 0 {
		t.Fatal("Zero kept the key")
	}
}

func TestAtRestUnknownSource(t *testing.T) {
	s := tempStore(t, newFakeKeyring())
	if _, err := s.AtRest(context.Background(), "nope", strings.Repeat("0", 32)); !errors.Is(err, ErrSource) {
		t.Fatalf("unknown source = %v", err)
	}
	if _, err := s.AtRest(context.Background(), SourceFile, "bad"); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("bad id = %v", err)
	}
}

func TestNoBackendUsesFile(t *testing.T) {
	kr := newFakeKeyring()
	s, err := New(Options{ConfigDir: filepath.Join(t.TempDir(), "damnbot"), Keyring: kr, BackendPresent: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	k, err := s.EnsureAtRest(context.Background())
	if err != nil || k.Source != SourceFile {
		t.Fatalf("no backend = %+v, %v", k, err)
	}
	kr.mu.Lock()
	n := len(kr.m)
	kr.mu.Unlock()
	if n != 0 {
		t.Fatalf("keyring was touched with no backend: %d items", n)
	}
}

func TestTransientFailsClosed(t *testing.T) {
	kr := newFakeKeyring()
	kr.setDown()
	s := tempStore(t, kr)
	if _, err := s.EnsureAtRest(context.Background()); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("transient = %v", err)
	}
	if got := files(t, s.dir); len(got) != 0 {
		t.Fatalf("file fallback after transient error: %v", got)
	}
}

func TestKeyringTimeout(t *testing.T) {
	kr := newFakeKeyring()
	ch := make(chan struct{})
	kr.blockOn(ch)
	defer close(ch)
	s := tempStore(t, kr)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.EnsureAtRest(ctx); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("timeout = %v", err)
	}
}

func TestRotationCrashSafe(t *testing.T) {
	kr := newFakeKeyring()
	kr.unsupported = true
	s := tempStore(t, kr)
	ctx := context.Background()
	old, err := s.EnsureAtRest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.StageAtRest(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.EnsureAtRest(ctx); err != nil || got.ID != old.ID {
		t.Fatalf("after stage Ensure = %+v, %v", got, err)
	}
	if _, err := s.AtRest(ctx, next.Source, next.ID); err != nil {
		t.Fatalf("staged key: %v", err)
	}
	if err := s.CommitAtRest(ctx, next); err != nil {
		t.Fatal(err)
	}
	if got, err := s.EnsureAtRest(ctx); err != nil || got.ID != next.ID {
		t.Fatalf("after commit Ensure = %+v, %v", got, err)
	}
	if _, err := s.AtRest(ctx, old.Source, old.ID); err != nil {
		t.Fatalf("old key before retire: %v", err)
	}
	if err := s.RetireAtRest(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AtRest(ctx, old.Source, old.ID); !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("retired old = %v", err)
	}
	if got, err := s.EnsureAtRest(ctx); err != nil || got.ID != next.ID {
		t.Fatalf("after retire Ensure = %+v, %v", got, err)
	}
}

func TestRetireRefusesCurrent(t *testing.T) {
	kr := newFakeKeyring()
	kr.unsupported = true
	s := tempStore(t, kr)
	k, err := s.EnsureAtRest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RetireAtRest(context.Background(), k); !errors.Is(err, ErrKeySource) {
		t.Fatalf("retire current = %v", err)
	}
}

func TestFirstUseRace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "damnbot")
	kr := newFakeKeyring()
	kr.unsupported = true
	ctx := context.Background()
	ids := make(chan string, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			s, err := New(Options{ConfigDir: dir, Keyring: kr})
			if err != nil {
				t.Error(err)
				return
			}
			k, err := s.EnsureAtRest(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- k.ID
		})
	}
	wg.Wait()
	close(ids)
	var want string
	for id := range ids {
		if want == "" {
			want = id
		} else if id != want {
			t.Fatalf("concurrent first use produced %s and %s", want, id)
		}
	}
	if got := files(t, dir); len(got) != 1 {
		t.Fatalf("orphan key files: %v", got)
	}
}
