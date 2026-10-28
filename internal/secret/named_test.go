package secret

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type namedFakeKeyring struct {
	vals map[string]string
	fail bool
}

func (k *namedFakeKeyring) Get(service, user string) (string, error) {
	if k.fail {
		return "", errors.New("keyring unavailable")
	}
	v, ok := k.vals[service+"/"+user]
	if !ok {
		return "", errors.New("not found")
	}
	return v, nil
}

func (k *namedFakeKeyring) Set(service, user, value string) error {
	if k.fail {
		return errors.New("keyring unavailable")
	}
	k.vals[service+"/"+user] = value
	return nil
}

func (k *namedFakeKeyring) Delete(service, user string) error {
	if k.fail {
		return errors.New("keyring unavailable")
	}
	delete(k.vals, service+"/"+user)
	return nil
}

func namedStore(t *testing.T, kr *namedFakeKeyring) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- test dir needs 0700
		t.Fatal(err)
	}
	s, err := New(Options{ConfigDir: dir, Keyring: kr})
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestNamedSetGetList(t *testing.T) {
	kr := &namedFakeKeyring{vals: map[string]string{}}
	s, dir := namedStore(t, kr)
	ctx := t.Context()
	if err := s.Set(ctx, "openrouter", "s3cr3t-value"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, "openrouter"); err != nil || got != "s3cr3t-value" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 || list[0].Name != "openrouter" || list[0].Fingerprint == "" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	idx, err := fs.ReadFile(os.DirFS(dir), "secrets.index")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(idx), "s3cr3t-value") {
		t.Fatal("index contains the secret value")
	}
	fi, err := os.Stat(filepath.Join(dir, "secrets.index"))
	if err != nil || (fi.Mode().Perm() != 0o600 && runtime.GOOS != "windows") {
		t.Fatalf("index mode = %v, %v", fi.Mode().Perm(), err)
	}
	if err := s.Set(ctx, "openrouter", "other"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("duplicate Set = %v, want ErrSecretExists", err)
	}
}

func TestNamedRotate(t *testing.T) {
	kr := &namedFakeKeyring{vals: map[string]string{}}
	s, _ := namedStore(t, kr)
	ctx := t.Context()
	if err := s.Set(ctx, "token", "one"); err != nil {
		t.Fatal(err)
	}
	before, berr := s.List()
	if berr != nil {
		t.Fatal(berr)
	}
	if err := s.Rotate(ctx, "token", "two"); err != nil {
		t.Fatal(err)
	}
	after, aerr := s.List()
	if aerr != nil {
		t.Fatal(aerr)
	}
	if len(before) != 1 || len(after) != 1 || before[0].Fingerprint == after[0].Fingerprint {
		t.Fatalf("fingerprint did not change: %+v %+v", before, after)
	}
	got, gerr := s.Get(ctx, "token")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if got != "two" {
		t.Fatalf("Get after rotate = %q", got)
	}
	if err := s.Rotate(ctx, "missing", "x"); !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("rotate missing = %v", err)
	}
}

func TestNamedKeyringFailureWritesNothing(t *testing.T) {
	kr := &namedFakeKeyring{vals: map[string]string{}, fail: true}
	s, dir := namedStore(t, kr)
	if err := s.Set(t.Context(), "token", "value"); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("Set = %v, want ErrKeyUnavailable", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets.index")); !os.IsNotExist(err) {
		t.Fatal("index written despite keyring failure")
	}
}
