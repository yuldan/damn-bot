package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/secret"
)

// fakeNamedStore is one profile's named-secret view for the rotation tests.
type fakeNamedStore struct {
	name        string
	entries     []secret.NamedEntry
	values      map[string]string
	rotates     []string
	unavailable bool
	failRotate  bool
}

func newFakeNamed(profile, name, value string) *fakeNamedStore {
	f := &fakeNamedStore{name: profile, values: map[string]string{}}
	f.store(name, value)
	return f
}

func (f *fakeNamedStore) store(name, value string) {
	f.values[name] = value
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
	for i := range f.entries {
		if f.entries[i].Name == name {
			f.entries[i].Fingerprint = sum
			return
		}
	}
	f.entries = append(f.entries, secret.NamedEntry{Name: name, Fingerprint: sum})
}

func (f *fakeNamedStore) List() ([]secret.NamedEntry, error) { return f.entries, nil }

func (f *fakeNamedStore) Get(_ context.Context, name string) (string, error) {
	if f.unavailable {
		return "", secret.ErrKeyUnavailable
	}
	v, ok := f.values[name]
	if !ok {
		return "", secret.ErrKeyMissing
	}
	return v, nil
}

func (f *fakeNamedStore) Rotate(_ context.Context, name, value string) error {
	f.rotates = append(f.rotates, value)
	if f.failRotate {
		return errors.New("secrets.index: write failed")
	}
	f.store(name, value)
	return nil
}

func TestNamedRotateAllProfiles(t *testing.T) {
	t.Parallel()
	def := newFakeNamed("", "openrouter", "old-default")
	p1 := newFakeNamed("p1", "openrouter", "old-p1")
	p2 := newFakeNamed("p2", "openrouter", "old-p2")
	fakes := []*fakeNamedStore{def, p1, p2}
	stores := []secretProfile{{"", def}, {"p1", p1}, {"p2", p2}}
	before := make([]string, len(fakes))
	for i, f := range fakes {
		before[i] = f.entries[0].Fingerprint
	}
	var out, errb bytes.Buffer
	if code := rotateAllProfiles(t.Context(), stores, "openrouter", "new-value", &out, &errb); code != 0 {
		t.Fatalf("rotate all = %d, stderr %s", code, errb.String())
	}
	for i, f := range fakes {
		if got := f.values["openrouter"]; got != "new-value" {
			t.Errorf("%s value = %q", secretProfileLabel(f.name), got)
		}
		if f.entries[0].Fingerprint == before[i] {
			t.Errorf("%s fingerprint unchanged", secretProfileLabel(f.name))
		}
		if len(f.rotates) != 1 {
			t.Errorf("%s rotate calls = %v", secretProfileLabel(f.name), f.rotates)
		}
	}
	for _, want := range []string{"rotated default\n", "rotated p1\n", "rotated p2\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout %q missing %q", out.String(), want)
		}
	}
	if strings.Contains(out.String()+errb.String(), "new-value") {
		t.Fatal("output leaked the secret value")
	}
}

func TestNamedRotateAllProfilesPartialFailure(t *testing.T) {
	t.Parallel()
	ok := newFakeNamed("", "openrouter", "old-default")
	bad := newFakeNamed("p1", "openrouter", "old-p1")
	bad.failRotate = true
	spare := newFakeNamed("p2", "token", "other")
	var out, errb bytes.Buffer
	stores := []secretProfile{{"", ok}, {"p1", bad}, {"p2", spare}}
	if code := rotateAllProfiles(t.Context(), stores, "openrouter", "new-value", &out, &errb); code != 1 {
		t.Fatalf("rotate all = %d, want 1", code)
	}
	if got := ok.values["openrouter"]; got != "new-value" {
		t.Errorf("healthy profile value = %q", got)
	}
	if got := bad.values["openrouter"]; got != "old-p1" {
		t.Errorf("failed profile value = %q, want the old value", got)
	}
	if len(bad.rotates) != 2 || bad.rotates[1] != "old-p1" {
		t.Errorf("failed profile calls = %v, want a rollback call with the old value", bad.rotates)
	}
	if got := spare.values["token"]; got != "other" {
		t.Errorf("untouched profile value = %q", got)
	}
	if !strings.Contains(out.String(), "rotated default\n") || !strings.Contains(out.String(), "skipped p2: no entry\n") {
		t.Errorf("stdout = %q", out.String())
	}
	if !strings.Contains(errb.String(), "profile p1") {
		t.Errorf("stderr = %q", errb.String())
	}
}

func TestSecretsListRefsMatrix(t *testing.T) {
	cfgDir := secretTestHome(t)
	for profile, body := range map[string]string{
		"":     "openrouter\tdef-fp\ntoken\tdef-token\n",
		"work": "openrouter\twork-fp\n",
		"beta": "openrouter\tbeta-fp\n",
	} {
		dir := cfgDir
		if profile != "" {
			dir = filepath.Join(cfgDir, "profiles", profile)
		}
		writeSecretIndex(t, dir, body)
	}
	code, out, errOut := runArgs(t, "setup", "secrets", "ls", "--refs")
	if code != 0 {
		t.Fatalf("ls --refs = (%d, %s)", code, errOut)
	}
	want := "openrouter\tbeta\tbeta-fp\n" +
		"openrouter\tdefault\tdef-fp\n" +
		"openrouter\twork\twork-fp\n" +
		"token\tdefault\tdef-token\n"
	if out != want {
		t.Fatalf("ls --refs = %q, want %q", out, want)
	}
	plainCode, plainOut, plainErr := runArgs(t, "setup", "secrets", "ls")
	if plainCode != 0 || plainOut != "openrouter\tdef-fp\ntoken\tdef-token\n" {
		t.Fatalf("ls = (%d, %q, %s), want only the active profile", plainCode, plainOut, plainErr)
	}
}

func TestSecretsRotateAllProfilesRequiresKeyring(t *testing.T) {
	t.Parallel()
	one := newFakeNamed("", "openrouter", "old-default")
	one.unavailable = true
	two := newFakeNamed("p1", "openrouter", "old-p1")
	two.unavailable = true
	var out, errb bytes.Buffer
	stores := []secretProfile{{"", one}, {"p1", two}}
	if code := rotateAllProfiles(t.Context(), stores, "openrouter", "new-value", &out, &errb); code != 1 {
		t.Fatalf("rotate all without keyring = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "OS keychain unavailable; nothing written") {
		t.Fatalf("stderr = %q", errb.String())
	}
	if out.String() != "" {
		t.Fatalf("stdout = %q, want nothing", out.String())
	}
	if one.values["openrouter"] != "old-default" || two.values["openrouter"] != "old-p1" {
		t.Errorf("values changed: %v %v", one.values, two.values)
	}
	for _, f := range []*fakeNamedStore{one, two} {
		if len(f.rotates) != 0 {
			t.Errorf("%s calls = %v, want none", secretProfileLabel(f.name), f.rotates)
		}
	}
}

// secretTestHome points the config directories at a temporary home.
func secretTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, kv := range [][2]string{
		{"HOME", home},
		{"USERPROFILE", home},
		{"XDG_CONFIG_HOME", home},
		{"XDG_DATA_HOME", home},
		{"APPDATA", filepath.Join(home, "AppData", "Roaming")},
		{"LOCALAPPDATA", filepath.Join(home, "AppData", "Local")},
	} {
		t.Setenv(kv[0], kv[1])
	}
	setActiveProfile("")
	t.Cleanup(func() { setActiveProfile("") })
	loader, err := config.New(config.OS())
	if err != nil {
		t.Fatal(err)
	}
	paths, err := loader.Paths("")
	if err != nil {
		t.Fatal(err)
	}
	return paths.ConfigDir
}

func writeSecretIndex(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.index"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
