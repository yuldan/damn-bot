package secret

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCodecRoundTripAndScope(t *testing.T) {
	dek := bytes.Repeat([]byte{7}, KeySize)
	c, err := NewCodec(dek)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := c.Seal("events.content", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("hello")) {
		t.Fatal("plaintext in envelope")
	}
	got, err := c.Open("events.content", sealed)
	if err != nil || string(got) != "hello" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if _, err := c.Open("events.payload", sealed); err == nil {
		t.Fatal("scope AAD not enforced")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := c.Open("events.content", sealed); err == nil {
		t.Fatal("tamper accepted")
	}
}

func TestStorageKeyRoundTrip(t *testing.T) {
	kr := newFakeKeyring()
	kr.unsupported = true
	st := tempStore(t, kr)
	dir := t.TempDir()
	ctx := context.Background()
	c, err := OpenCodec(ctx, dir, st, true)
	if err != nil {
		t.Fatal(err)
	}
	sealed, serr := c.Seal("x", []byte("v"))
	if serr != nil {
		t.Fatal(serr)
	}
	if got, gerr := c.Open("x", sealed); gerr != nil || string(got) != "v" {
		t.Fatalf("codec = %q, %v", got, gerr)
	}
	fi, err := os.Stat(StorageKeyPath(dir))
	if err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("storage key = %v, %v", fi, err)
	}
	c2, err := OpenCodec(ctx, dir, st, false)
	if err != nil {
		t.Fatal(err)
	}
	if c2.ID() != c.ID() {
		t.Fatalf("ids %s != %s", c2.ID(), c.ID())
	}
	if _, err := OpenCodec(ctx, filepath.Join(dir, "missing"), st, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing = %v", err)
	}
	// Wrong KEK: a different key store must fail the unwrap.
	other := tempStore(t, newFakeKeyring())
	if _, err := OpenCodec(ctx, dir, other, false); err == nil {
		t.Fatal("wrong KEK unwrapped storage.key")
	}
}
