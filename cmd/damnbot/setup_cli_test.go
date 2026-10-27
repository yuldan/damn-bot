package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func setupFresh(t *testing.T) string {
	t.Helper()
	file, _ := configTestHome(t, "")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "supersecret")
	return file
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
	file := setupFresh(t)
	code, out, errOut := runArgs(t, "setup", "--yes", "--model", "openai/gpt-test")
	if code != 0 {
		t.Fatalf("setup = (%d, %s)", code, errOut)
	}
	for _, secret := range []string{"supersecret", "OPENAI_API_KEY=supersecret"} {
		if strings.Contains(out, secret) || strings.Contains(errOut, secret) {
			t.Fatalf("setup leaked a secret value")
		}
	}
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
}

func TestSetupInvalidConfigNotOverwritten(t *testing.T) {
	file, _ := configTestHome(t, "[loop]\nmax_iterations = 99\n")
	before := setupHash(t, file)
	code, _, errOut := runArgs(t, "setup", "--yes", "--model", "openai/gpt-test")
	if code != 1 || !strings.Contains(errOut, file) {
		t.Fatalf("setup = (%d, %s), want exit 1 naming the file", code, errOut)
	}
	if after := setupHash(t, file); after != before {
		t.Fatal("setup overwrote an invalid config")
	}
}

func TestSetupNonTTYWithoutYes(t *testing.T) {
	file := setupFresh(t)
	if code, _, errOut := runArgs(t, "setup", "--model", "openai/gpt-test"); code != 2 {
		t.Fatalf("setup = (%d, %s), want exit 2", code, errOut)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("setup wrote config without --yes in a non-TTY")
	}
}

func TestSetupIdempotent(t *testing.T) {
	file := setupFresh(t)
	runArgs(t, "setup", "--yes", "--model", "openai/gpt-test")
	before := setupHash(t, file)
	if code, out, errOut := runArgs(t, "setup", "--yes"); code != 0 || !strings.Contains(out, "unchanged") {
		t.Fatalf("second setup = (%d, %q, %s)", code, out, errOut)
	}
	if after := setupHash(t, file); after != before {
		t.Fatal("setup rewrote an already-valid config")
	}
}

func TestSetupRejectsBadModel(t *testing.T) {
	file := setupFresh(t)
	if code, _, _ := runArgs(t, "setup", "--yes", "--model", "nope"); code != 2 {
		t.Fatalf("bad model exit = %d, want 2", code)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("setup wrote config for an invalid model ref")
	}
}
