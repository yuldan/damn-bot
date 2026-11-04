package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// fakeVoice records the seam calls and models a cancellable synthesis.
type fakeVoice struct {
	mu      sync.Mutex
	st      VoiceStatus
	setWake []bool
	spoken  []string
	cancel  context.CancelFunc
	block   bool
}

func (f *fakeVoice) Status(context.Context) VoiceStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

func (f *fakeVoice) SetVoice(_ context.Context, on bool) VoiceStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !on && f.cancel != nil {
		f.cancel()
		f.cancel = nil
	}
	f.st.Enabled = on && f.st.TTSReason == ""
	if !on {
		f.st.Wake = false
	}
	return f.st
}

func (f *fakeVoice) SetWake(_ context.Context, on bool) VoiceStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setWake = append(f.setWake, on)
	f.st.Wake = on && f.st.Enabled && f.st.WakeReason == ""
	return f.st
}

func (f *fakeVoice) Speak(ctx context.Context, text string) (string, error) {
	f.mu.Lock()
	f.spoken = append(f.spoken, text)
	block := f.block
	f.mu.Unlock()
	if !block {
		return "audio/wav audio (12 bytes)", nil
	}
	cctx, cancel := context.WithCancel(ctx)
	f.mu.Lock()
	f.cancel = cancel
	f.mu.Unlock()
	<-cctx.Done()
	return "", errors.New("voice: synthesis stopped: context canceled")
}

func (f *fakeVoice) spokenTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.spoken...)
}

func (f *fakeVoice) wakeToggles() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.setWake...)
}

// checkTUIVoiceCancel proves /voice off cancels a running synthesis in the TUI.
func checkTUIVoiceCancel(t *testing.T, m *Model, fv *fakeVoice) {
	t.Helper()
	fv.mu.Lock()
	fv.block = true
	fv.mu.Unlock()
	m.input.SetValue("/voice tts panjang")
	cmd := m.submit()
	msgCh := make(chan tea.Msg, 1)
	go func() { msgCh <- cmd() }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		fv.mu.Lock()
		started := fv.cancel != nil
		fv.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("synthesis did not start")
		}
		<-time.After(time.Millisecond)
	}
	m.input.SetValue("/voice off")
	m.submit()
	select {
	case msg := <-msgCh:
		m.Update(msg)
		if !strings.Contains(m.View().Content, "voice tts refused") {
			t.Fatalf("stopped tts notice = %q", m.View().Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("/voice off did not cancel the running synthesis")
	}
}

// TestF6SLASH063Voice: /voice on|off|tts|status reaches the seam, reports an
// unavailable backend honestly, and off cancels a running synthesis.
func TestF6SLASH063Voice(t *testing.T) {
	m, _, _ := newSlashApp(t)
	fv := &fakeVoice{st: VoiceStatus{Enabled: true, TTSBackend: "piper", WakeEngine: "openwakeword"}}
	m.host.Voice = fv

	m.input.SetValue("/voice status")
	m.submit()
	if !strings.Contains(m.View().Content, "voice on; tts piper") {
		t.Fatalf("status = %q", m.View().Content)
	}
	m.input.SetValue("/voice off")
	m.submit()
	if !strings.Contains(m.View().Content, "voice off; tts piper") {
		t.Fatalf("off = %q", m.View().Content)
	}
	m.input.SetValue("/voice on")
	m.submit()
	m.input.SetValue("/voice tts halo dunia")
	cmd := m.submit()
	if cmd == nil {
		t.Fatal("/voice tts returned no command")
	}
	msg := cmd()
	if _, ok := msg.(voiceDoneMsg); !ok {
		t.Fatalf("tts msg = %T", msg)
	}
	m.Update(msg)
	if spoken := fv.spokenTexts(); len(spoken) != 1 || spoken[0] != "halo dunia" {
		t.Fatalf("spoken = %q", spoken)
	}
	if !strings.Contains(m.View().Content, "voice tts") {
		t.Fatalf("tts notice = %q", m.View().Content)
	}

	checkTUIVoiceCancel(t, m, fv)

	broken := &fakeVoice{st: VoiceStatus{TTSReason: "module_unavailable: no tts_backend configured"}}
	m2, _, _ := newSlashApp(t)
	m2.host.Voice = broken
	m2.input.SetValue("/voice on")
	m2.submit()
	if !strings.Contains(m2.View().Content, "voice refused:") || !strings.Contains(m2.View().Content, "module_unavailable") {
		t.Fatalf("unavailable voice = %q", m2.View().Content)
	}
	if broken.st.Enabled {
		t.Fatal("voice on without a backend")
	}
	if res := m2.runSlash(slashCommand{kind: slashVoice, args: []string{"bogus"}}); res != nil {
		t.Fatal("bogus /voice returned a command")
	}
	if !strings.Contains(m2.View().Content, "usage: /voice on|off|tts <text>|status") {
		t.Fatalf("usage = %q", m2.View().Content)
	}
}

// TestF6SLASH064Wake: /wake is opt-in, reports the installation message when
// the engine is absent, and never claims the listener is on.
func TestF6SLASH064Wake(t *testing.T) {
	m, _, _ := newSlashApp(t)
	fv := &fakeVoice{st: VoiceStatus{
		Enabled: true, WakeEngine: "porcupine",
		WakeReason: "voice/wake: engine binary is not installed; install it manually",
	}}
	m.host.Voice = fv
	m.input.SetValue("/wake status")
	m.submit()
	if !strings.Contains(m.View().Content, "wake porcupine off") || !strings.Contains(m.View().Content, "not installed") {
		t.Fatalf("status = %q", m.View().Content)
	}
	m.input.SetValue("/wake on")
	m.submit()
	if !strings.Contains(m.View().Content, "wake refused:") {
		t.Fatalf("refusal = %q", m.View().Content)
	}
	if fv.st.Wake {
		t.Fatal("wake on without an engine")
	}
	m.input.SetValue("/wake bogus")
	m.submit()
	if !strings.Contains(m.View().Content, "usage: /wake on|off|status") {
		t.Fatalf("usage = %q", m.View().Content)
	}

	ok := &fakeVoice{st: VoiceStatus{Enabled: true, WakeEngine: "openwakeword"}}
	m2, _, _ := newSlashApp(t)
	m2.host.Voice = ok
	m2.input.SetValue("/wake on")
	m2.submit()
	if !ok.st.Wake || !strings.Contains(m2.View().Content, "wake openwakeword on") {
		t.Fatalf("on = %q", m2.View().Content)
	}
	m2.input.SetValue("/wake off")
	m2.submit()
	if ok.st.Wake || !strings.Contains(m2.View().Content, "wake openwakeword off") {
		t.Fatalf("off = %q", m2.View().Content)
	}
	if setWake := ok.wakeToggles(); len(setWake) != 2 {
		t.Fatalf("SetWake calls = %v", setWake)
	}
}
