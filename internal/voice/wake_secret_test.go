//go:build !minimal

package voice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/voice/wake"
)

// TestF6MEDIA006PorcupineUsesSecret: the access key comes only from the
// secret seam, never from [modules.voice], and a missing secret fails closed.
func TestF6MEDIA006PorcupineUsesSecret(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	bin := filepath.Join(dir, "porcupine")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$WAKE_CAPTURE\"\nexit 7\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("WAKE_CAPTURE", capture)
	const secret = "pv-only-from-secrets" // #nosec G101 -- fake access key for the fixture
	ctx := context.Background()
	m := testModule(ctx, t, &Section{
		Enabled: true, WakeEngine: wake.EnginePorcupine, WakeBinary: bin, WakeKeyEnv: "PICOVOICE_ACCESS_KEY",
	}, Deps{Secrets: fakeSecrets{"PICOVOICE_ACCESS_KEY": secret}})
	e, err := m.wakeEngine()
	if err != nil {
		t.Fatalf("wakeEngine: %v", err)
	}
	err = e.Wait(ctx)
	if !errors.Is(err, wake.ErrCommand) {
		t.Fatalf("Wait err = %v, want ErrCommand", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the access key: %v", err)
	}
	raw, rerr := os.ReadFile(capture) //nolint:gosec // path comes from t.TempDir
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(raw), secret) {
		t.Fatalf("engine did not receive the secret: %q", raw)
	}

	noSecret := testModule(ctx, t, &Section{
		Enabled: true, WakeEngine: wake.EnginePorcupine, WakeBinary: bin, WakeKeyEnv: "PICOVOICE_ACCESS_KEY",
	}, Deps{Secrets: fakeSecrets{}})
	if _, err := noSecret.wakeEngine(); !errors.Is(err, wake.ErrMissingAsset) {
		t.Fatalf("missing secret err = %v, want ErrMissingAsset", err)
	}
}
