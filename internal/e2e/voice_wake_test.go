package e2e

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/voice"
)

// voiceFixtureSource is the external provider the boundary scenario drives. It
// records every argv, sleeps when asked, writes a WAV body to the {out} path,
// and exits 0 for a wake probe.
const voiceFixtureSource = `package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	log, err := os.OpenFile(os.Getenv("VOICE_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(1)
	}
	_, _ = log.WriteString(strings.Join(os.Args[1:], "\x00") + "\n")
	_ = log.Close()
	if ms, _ := strconv.Atoi(os.Getenv("VOICE_SLEEP_MS")); ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	if len(os.Args) > 1 && strings.HasSuffix(os.Args[1], ".audio") {
		_ = os.WriteFile(os.Args[1], []byte("RIFF0000WAVE"), 0o600)
		return
	}
	_, _ = os.Stdout.WriteString("RIFF0000WAVE")
}
`

func buildVoiceFixture(t *testing.T) (bin, logPath string) {
	t.Helper()
	bin, dir := buildGoFixture(t, voiceFixtureSource, "voice-fixture")
	return bin, filepath.Join(dir, "voice.log")
}

// TestVoiceWakeArgvBoundaryE2E exercises the two process boundaries P6b owns:
// a TTS command never passes through a shell, /voice off kills the running
// provider, and an absent wake engine keeps the microphone closed.
func TestVoiceWakeArgvBoundaryE2E(t *testing.T) {
	bin, logPath := buildVoiceFixture(t)
	t.Setenv("VOICE_LOG", logPath)
	dir := t.TempDir()
	checkArgvBoundary(t, bin, logPath, dir)
	checkVoiceOffCancels(t, bin, logPath)
	checkAbsentEngine(t, dir, bin)
}

// checkArgvBoundary proves the text reaches the provider as one exact argv
// element and that no shell metacharacter runs.
func checkArgvBoundary(t *testing.T, bin, logPath, dir string) {
	t.Helper()
	pwned := filepath.Join(dir, "pwned")
	text := "halo; touch " + pwned + " | touch " + pwned + "2 $(touch " + pwned + "3)"
	surf := voice.NewSurface(&voice.SurfaceConfig{
		Enabled: true, TTSBackend: "piper",
		TTSCommand: []string{bin, "{out}", "{text}"},
	})
	got, err := surf.Speak(context.Background(), text)
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if !strings.Contains(got, "audio/wav") {
		t.Fatalf("Speak = %q", got)
	}
	if _, serr := os.Stat(pwned); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("argv reached a shell: %v", serr)
	}
	raw, err := os.ReadFile(logPath) //nolint:gosec // path comes from t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), text) {
		t.Fatalf("provider argv = %q, want one exact text element", raw)
	}
}

// checkVoiceOffCancels proves "/voice off" kills the provider process instead
// of merely ignoring its output.
func checkVoiceOffCancels(t *testing.T, bin, logPath string) {
	t.Helper()
	t.Setenv("VOICE_SLEEP_MS", "5000")
	running := voice.NewSurface(&voice.SurfaceConfig{
		Enabled: true, TTSBackend: "piper",
		TTSCommand: []string{bin, "{out}", "{text}"},
	})
	done := make(chan error, 1)
	go func() {
		_, serr := running.Speak(context.Background(), "panjang")
		done <- serr
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, serr := os.ReadFile(logPath) //nolint:gosec // path comes from t.TempDir
		if serr == nil && strings.Count(string(raw), "\n") >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("provider did not start")
		}
		<-time.After(10 * time.Millisecond)
	}
	running.SetVoice(false)
	select {
	case err := <-done:
		if !errors.Is(err, voice.ErrSurfaceStopped) {
			t.Fatalf("running synthesis err = %v, want ErrSurfaceStopped", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("/voice off did not stop the provider process")
	}
}

// checkAbsentEngine proves a missing preset binary keeps the mic closed.
func checkAbsentEngine(t *testing.T, dir, bin string) {
	t.Helper()
	missing := filepath.Join(dir, "no-engine")
	wake := voice.NewSurface(&voice.SurfaceConfig{
		Enabled: true, WakeEngine: "openwakeword", WakeBinary: missing, WakeModel: "hey_hermes",
	})
	st := wake.SetWake(true)
	if st.Wake || st.WakeReason == "" {
		t.Fatalf("absent engine status = %+v", st)
	}
	if err := wake.WakeReady(); err == nil {
		t.Fatal("absent engine opened the microphone")
	}
	ok := voice.NewSurface(&voice.SurfaceConfig{
		Enabled: true, WakeEngine: "openwakeword", WakeBinary: bin, WakeModel: "hey_hermes",
	})
	ok.SetWake(true)
	if err := ok.WakeReady(); err != nil {
		t.Fatalf("WakeReady with an installed engine: %v", err)
	}
}
