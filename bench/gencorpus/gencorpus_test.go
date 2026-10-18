package gencorpus

import (
	"path/filepath"
	"testing"
)

// TestDeterministic: seed sama → digest sama; seed beda → digest beda (corpora.lock dapat di-pin).
func TestDeterministic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := Params{Seed: 3, Files: 300, MeanBytes: 2000}
	a, err := Generate(filepath.Join(dir, "a"), p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(filepath.Join(dir, "b"), p)
	if err != nil {
		t.Fatal(err)
	}
	p.Seed = 4
	c, err := Generate(filepath.Join(dir, "c"), p)
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest != b.Digest || a.Bytes != b.Bytes {
		t.Fatalf("seed sama, hasil beda: %+v vs %+v", a, b)
	}
	if a.Digest == c.Digest {
		t.Fatal("seed beda, digest sama")
	}
	if a.Binary != 3 || a.Minified != 1 || a.Ignored == 0 {
		t.Fatalf("komposisi salah: %+v", a)
	}
}

func TestGenerateRefusesExisting(t *testing.T) {
	t.Parallel()
	if _, err := Generate(t.TempDir(), Params{Seed: 1, Files: 1, MeanBytes: 10}); err == nil {
		t.Fatal("Generate ke direktori yang ada = nil error")
	}
}

func TestOrderVerbs(t *testing.T) {
	t.Parallel()
	if got := orderVerbs("a %d b %s c %%w %s"); got != "a %[3]d b %[1]s c %%w %[2]s" {
		t.Fatalf("orderVerbs = %q", got)
	}
}
