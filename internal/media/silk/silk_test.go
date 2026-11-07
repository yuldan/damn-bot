package silk

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// goldenPCM is four PCM16 samples (8 bytes), small enough to review by eye.
func goldenPCM() []byte {
	return []byte{0x01, 0x00, 0x02, 0x00, 0xfd, 0xff, 0x00, 0x00}
}

// helperBinary builds testdata/decoder into the test temp dir.
func helperBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "silk-decoder")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./testdata/decoder") // #nosec G204 -- fixed go tool
	if err := cmd.Run(); err != nil {
		t.Fatalf("build helper: %v", err)
	}
	return bin
}

func helperConfig(t *testing.T, mode string) *Config {
	t.Helper()
	return &Config{
		Argv: []string{helperBinary(t)},
		Env:  append(os.Environ(), "SILK_HELPER_MODE="+mode),
	}
}

// TestSilkDecode proves the success path and the exact PCM16 golden.
func TestSilkDecode(t *testing.T) {
	var out bytes.Buffer
	if err := Decode(t.Context(), []byte("silk-payload"), &out, helperConfig(t, "ok")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), goldenPCM()) {
		t.Fatalf("pcm = %x, want %x", out.Bytes(), goldenPCM())
	}
}

// TestSilkCorruptPayload proves failures are typed and write 0 bytes.
func TestSilkCorruptPayload(t *testing.T) {
	dir := t.TempDir()
	cases := []string{"fail", "empty", "huge"}
	for _, mode := range cases {
		var out bytes.Buffer
		err := Decode(t.Context(), []byte("bad"), &out, helperConfig(t, mode))
		if mode == "huge" {
			if err == nil {
				t.Fatalf("%s: expected an error", mode)
			}
		} else if !errors.Is(err, ErrSilkCorrupt) {
			t.Fatalf("%s = %v, want ErrSilkCorrupt", mode, err)
		}
		if out.Len() != 0 {
			t.Fatalf("%s wrote %d bytes to out on failure", mode, out.Len())
		}
		entries, rerr := os.ReadDir(dir)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if len(entries) != 0 {
			t.Fatalf("%s left temp files: %v", mode, entries)
		}
	}
}

// TestSilkDecoderMissing proves a missing binary fails closed with 0 bytes.
func TestSilkDecoderMissing(t *testing.T) {
	var out bytes.Buffer
	cfg := &Config{Argv: []string{filepath.Join(t.TempDir(), "no-such-silk-decoder")}}
	if err := Decode(t.Context(), []byte("x"), &out, cfg); !errors.Is(err, ErrDecoderMissing) {
		t.Fatalf("missing decoder = %v, want ErrDecoderMissing", err)
	}
	if out.Len() != 0 {
		t.Fatalf("missing decoder wrote %d bytes", out.Len())
	}
	if err := Decode(context.Background(), []byte("x"), &out, nil); !errors.Is(err, ErrDecoderMissing) {
		t.Fatalf("nil config = %v", err)
	}
}
