package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/secret"
)

func TestCreateVerify(t *testing.T) {
	root := t.TempDir()
	data, cfg := filepath.Join(root, "data"), filepath.Join(root, "cfg")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: data, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if cerr := s.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if werr := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte("model = \"x\"\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	key := testKey(t)
	res, err := Create(context.Background(), &CreateOptions{
		DataDir: data, ConfigDir: cfg, App: "test", Key: key, Now: time.Unix(1_700_000_000, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	checkResult(t, &res, key)
	h, m, err := Verify(context.Background(), res.Path, key)
	if err != nil {
		t.Fatal(err)
	}
	checkManifest(t, &h, &m, key)
	other := testKey(t)
	if _, _, err := Verify(context.Background(), res.Path, other); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong key = %v", err)
	}
	tamper(t, res.Path, key)
}

func checkResult(t *testing.T, res *Result, key secret.BackupKey) {
	t.Helper()
	if res.Size == 0 || res.Header.RecipientID != key.ID || res.Header.ManifestSHA256 == "" {
		t.Fatalf("result %+v", res)
	}
	if runtime.GOOS == "windows" {
		return
	}
	for _, p := range []string{res.Path, filepath.Dir(res.Path)} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if fi.IsDir() {
			want = 0o700
		}
		if fi.Mode().Perm() != want {
			t.Fatalf("%s mode = %04o, want %04o", p, fi.Mode().Perm(), want)
		}
	}
}

func checkManifest(t *testing.T, h *Header, m *Manifest, key secret.BackupKey) {
	t.Helper()
	if h.RecipientID != key.ID || len(m.Entries) != 2 || m.Entries[0].Path != "config.toml" || m.Entries[1].Path != "damnbot.db" {
		t.Fatalf("header %+v manifest %+v", h, m)
	}
}

func tamper(t *testing.T, path string, key secret.BackupKey) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 1
	if err := os.WriteFile(path, raw, 0o600); err != nil { //nolint:gosec // explicit test archive path
		t.Fatal(err)
	}
	if _, _, err := Verify(context.Background(), path, key); err == nil {
		t.Fatal("tampered archive verified")
	}
}
