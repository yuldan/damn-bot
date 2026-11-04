package wake

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// fixture writes one executable that records its argv in $WAKE_CAPTURE and
// exits with $WAKE_EXIT (default 0). POSIX only, like the other shell fixtures.
func fixture(t *testing.T) (bin, capture string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	dir := t.TempDir()
	capture = filepath.Join(dir, "argv.txt")
	bin = filepath.Join(dir, "wake-fixture")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$WAKE_CAPTURE\"\nexit \"${WAKE_EXIT:-0}\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatalf("write fixture: %v", err)
	}
	return bin, capture
}

func captured(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // path comes from t.TempDir
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}

// TestF6MEDIA004OpenWakeWord: the preset passes the configured model, fails
// closed when the CLI is absent, and never downloads anything.
func TestF6MEDIA004OpenWakeWord(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "openwakeword")
	if _, err := New(&Config{Enabled: true, Engine: EngineOpenWakeWord, Binary: missing, Model: "hey_hermes"}); !errors.Is(err, ErrBinaryMissing) {
		t.Fatalf("missing binary err = %v, want ErrBinaryMissing", err)
	} else if !strings.Contains(err.Error(), "install") || !strings.Contains(err.Error(), "no automatic download") {
		t.Fatalf("missing binary message is not actionable: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("absent engine created %d files; nothing may be downloaded", len(entries))
	}
	if _, merr := New(&Config{Enabled: true, Engine: EngineOpenWakeWord, Binary: missing}); !errors.Is(merr, ErrMissingAsset) {
		t.Fatalf("missing model err = %v, want ErrMissingAsset", merr)
	}

	bin, capture := fixture(t)
	t.Setenv("WAKE_CAPTURE", capture)
	e, err := New(&Config{Enabled: true, Engine: EngineOpenWakeWord, Binary: bin, Model: "hey_hermes"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !e.Enabled() || e.EngineName() != EngineOpenWakeWord {
		t.Fatalf("engine = %q enabled=%v", e.EngineName(), e.Enabled())
	}
	if err := e.Wait(t.Context()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	args := captured(t, capture)
	if !contains(args, "--model") || !contains(args, "hey_hermes") {
		t.Fatalf("argv = %q, want --model hey_hermes", args)
	}
}

// TestF6MEDIA005SherpaKWS: the open vocabulary comes from wake_keywords and a
// missing KWS binary keeps the microphone closed.
func TestF6MEDIA005SherpaKWS(t *testing.T) {
	if _, err := New(&Config{Enabled: true, Engine: EngineSherpa, Binary: filepath.Join(t.TempDir(), "no-kws"), Keywords: []string{"halo"}}); !errors.Is(err, ErrBinaryMissing) {
		t.Fatalf("missing binary err = %v", err)
	}
	if _, err := New(&Config{Enabled: true, Engine: EngineSherpa, Binary: "sherpa-onnx-kws"}); !errors.Is(err, ErrMissingAsset) {
		t.Fatalf("missing keywords err = %v", err)
	}
	bin, capture := fixture(t)
	t.Setenv("WAKE_CAPTURE", capture)
	e, err := New(&Config{Enabled: true, Engine: EngineSherpa, Binary: bin, Keywords: []string{"halo bot", "hey bot"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := e.Wait(t.Context()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	args := captured(t, capture)
	if !contains(args, "--keywords") || !contains(args, "halo bot,hey bot") {
		t.Fatalf("argv = %q, want one joined --keywords element", args)
	}
}

// TestF6MEDIA006Porcupine: the access key is required, redacted from errors,
// and never invented by the adapter.
func TestF6MEDIA006Porcupine(t *testing.T) {
	if _, err := New(&Config{Enabled: true, Engine: EnginePorcupine, Binary: "porcupine"}); !errors.Is(err, ErrMissingAsset) {
		t.Fatalf("missing key err = %v, want ErrMissingAsset", err)
	} else if !strings.Contains(err.Error(), "PICOVOICE_ACCESS_KEY") {
		t.Fatalf("missing key message must name the secret: %v", err)
	}
	bin, capture := fixture(t)
	t.Setenv("WAKE_CAPTURE", capture)
	t.Setenv("WAKE_EXIT", "7")
	const secret = "pv-secret-do-not-log"
	e, err := New(&Config{Enabled: true, Engine: EnginePorcupine, Binary: bin, AccessKey: secret})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = e.Wait(t.Context())
	if !errors.Is(err, ErrCommand) {
		t.Fatalf("err = %v, want ErrCommand", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the access key: %v", err)
	}
	args := captured(t, capture)
	if !contains(args, "--access-key") || !contains(args, secret) {
		t.Fatalf("argv = %q, want the secret passed to the engine", args)
	}
}

func contains(list []string, want string) bool { return slices.Contains(list, want) }
