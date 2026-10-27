package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func setupFile(t *testing.T, e *Env) string {
	t.Helper()
	return filepath.Join(e.ConfigDir(), "config.toml")
}

func setupHash(t *testing.T, file string) string {
	t.Helper()
	b, err := fs.ReadFile(os.DirFS(filepath.Dir(file)), filepath.Base(file))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestSetupFreshYes(t *testing.T) {
	t.Parallel()
	e := New(t)
	start := time.Now()
	res := e.ExecEnv([]string{"OPENAI_API_KEY=supersecret"}, "setup", "--yes", "--model", "openai/gpt-test")
	res.RequireOK(t)
	if elapsed := time.Since(start); elapsed > 60*time.Second {
		t.Fatalf("wizard took %s, want <= 60 s", elapsed)
	}
	file := setupFile(t, e)
	b, err := fs.ReadFile(os.DirFS(filepath.Dir(file)), filepath.Base(file))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "[provider]\nmodel = \"openai/gpt-test\"\n" {
		t.Fatalf("config = %q", got)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(file)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, %v", fi.Mode().Perm(), err)
		}
	}
	if strings.Contains(res.Stdout, "supersecret") || strings.Contains(res.Stderr, "supersecret") {
		t.Fatal("setup printed the secret value")
	}
}

func TestSetupInvalidConfigNotOverwritten(t *testing.T) {
	t.Parallel()
	e := New(t)
	if err := os.MkdirAll(e.ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	file := setupFile(t, e)
	if err := os.WriteFile(file, []byte("[loop]\nmax_iterations = 99\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := setupHash(t, file)
	res := e.Exec("setup", "--yes", "--model", "openai/gpt-test")
	if res.Code != 1 || !strings.Contains(res.Stderr, file) {
		t.Fatalf("setup = (%d, %s), want exit 1 naming the file", res.Code, res.Stderr)
	}
	if after := setupHash(t, file); after != before {
		t.Fatal("setup overwrote an invalid config")
	}
}

func TestSetupNonTTYWithoutYes(t *testing.T) {
	t.Parallel()
	e := New(t)
	res := e.Exec("setup", "--model", "openai/gpt-test")
	if res.Code != 2 {
		t.Fatalf("exit = %d, want 2", res.Code)
	}
	if _, err := os.Stat(setupFile(t, e)); !os.IsNotExist(err) {
		t.Fatal("setup wrote config without --yes")
	}
}

func TestSetupIdempotent(t *testing.T) {
	t.Parallel()
	e := New(t)
	e.Exec("setup", "--yes", "--model", "openai/gpt-test").RequireOK(t)
	file := setupFile(t, e)
	before := setupHash(t, file)
	res := e.Exec("setup", "--yes")
	res.RequireOK(t)
	if !strings.Contains(res.Stdout, "unchanged") {
		t.Fatalf("second setup = %s", res.Stdout)
	}
	if after := setupHash(t, file); after != before {
		t.Fatal("setup rewrote an already-valid config")
	}
}
