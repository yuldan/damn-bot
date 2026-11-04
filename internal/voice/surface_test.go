package voice

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// surfaceFixture writes one executable that marks its start, sleeps, and then
// emits a WAV body.
func surfaceFixture(t *testing.T) (bin, marker string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	dir := t.TempDir()
	marker = filepath.Join(dir, "started")
	bin = filepath.Join(dir, "tts")
	script := "#!/bin/sh\ntouch \"$SURFACE_MARKER\"\nsleep 5\nprintf 'RIFF0000WAVE'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	return bin, marker
}

// TestSurfaceVoiceOffCancelsSynthesis is the real negative control behind
// /voice off: the external provider process is killed, not merely ignored.
func TestSurfaceVoiceOffCancelsSynthesis(t *testing.T) {
	bin, marker := surfaceFixture(t)
	t.Setenv("SURFACE_MARKER", marker)
	s := NewSurface(&SurfaceConfig{
		Enabled: true, TTSBackend: "piper", TTSCommand: []string{bin},
	})
	done := make(chan error, 1)
	go func() {
		_, err := s.Speak(t.Context(), "panjang")
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("synthesis did not start")
		}
		<-time.After(time.Millisecond)
	}
	st := s.SetVoice(false)
	if st.Enabled {
		t.Fatal("/voice off left voice enabled")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrSurfaceStopped) {
			t.Fatalf("Speak err = %v, want ErrSurfaceStopped", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("/voice off did not kill the provider process")
	}
}

// TestSurfaceStatusHonest: a missing backend is reported with its reason and
// wake never turns on without an installed engine.
func TestSurfaceStatusHonest(t *testing.T) {
	s := NewSurface(&SurfaceConfig{Enabled: true, WakeEngine: "porcupine"})
	st := s.Status()
	if st.TTSReason == "" || !strings.Contains(st.TTSReason, "module_unavailable") {
		t.Fatalf("tts reason = %q", st.TTSReason)
	}
	if st.WakeReason == "" || !strings.Contains(st.WakeReason, "PICOVOICE_ACCESS_KEY") {
		t.Fatalf("wake reason = %q", st.WakeReason)
	}
	if st.Enabled == false {
		t.Fatal("configured voice should be enabled")
	}
	if got := s.SetWake(true); got.Wake || s.WakeReady() == nil {
		t.Fatalf("wake opened without an engine: %+v", got)
	}
	off := NewSurface(&SurfaceConfig{Enabled: false, WakeEngine: "openwakeword", WakeBinary: "x", WakeModel: "m"})
	if st := off.SetWake(true); st.Wake || !strings.Contains(st.WakeReason, "voice module is off") {
		t.Fatalf("wake without module opt-in = %+v", st)
	}
}
