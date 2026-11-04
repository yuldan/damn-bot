package gateway_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
)

type fakeVoice struct {
	mu       sync.Mutex
	st       gateway.VoiceStatus
	statuses int
	setWake  []bool
	spoken   []string
	cancel   context.CancelFunc
	block    bool
	speakErr error
}

func (f *fakeVoice) Status(context.Context) gateway.VoiceStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses++
	return f.st
}

func (f *fakeVoice) SetVoice(_ context.Context, on bool) gateway.VoiceStatus {
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

func (f *fakeVoice) SetWake(_ context.Context, on bool) gateway.VoiceStatus {
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
	err := f.speakErr
	f.mu.Unlock()
	if block {
		cctx, cancel := context.WithCancel(ctx)
		f.mu.Lock()
		f.cancel = cancel
		f.mu.Unlock()
		<-cctx.Done()
		return "", errors.New("voice: synthesis stopped: context canceled")
	}
	return "audio/wav audio (12 bytes)", err
}

func (f *fakeVoice) snap() (setWake []bool, spoken []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.setWake...), append([]string(nil), f.spoken...)
}

// checkOffCancels proves /voice off cancels a running synthesis.
func checkOffCancels(ctx context.Context, t *testing.T, c *gateway.Commands, fv *fakeVoice, owner *gateway.Ticket) {
	t.Helper()
	speak := make(chan error, 1)
	go func() {
		_, err := fv.Speak(ctx, "panjang")
		speak <- err
	}()
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
	if res := c.Dispatch(ctx, owner, "/voice off"); !strings.Contains(res.Reply, "voice off") {
		t.Fatalf("off while speaking = %q", res.Reply)
	}
	select {
	case err := <-speak:
		if err == nil {
			t.Fatal("running synthesis survived /voice off")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("/voice off did not cancel the running synthesis")
	}
}

// TestF6SLASH063Voice covers /voice on|off|tts|status, the persisted toggle,
// and the refusal to claim on without a backend.
func TestF6SLASH063Voice(t *testing.T) {
	fv := &fakeVoice{st: gateway.VoiceStatus{Enabled: true, TTSBackend: "piper", WakeEngine: "openwakeword"}}
	settings := &fakeSettings{}
	c := gateway.NewCommands(&gateway.CommandOptions{Voice: fv, Settings: settings})
	owner := testTicket(gateway.RoleOwner)
	ctx := context.Background()

	if res := c.Dispatch(ctx, owner, "/voice status"); !strings.Contains(res.Reply, "voice on; tts piper") {
		t.Fatalf("status = %q", res.Reply)
	}
	if res := c.Dispatch(ctx, owner, "/voice off"); !strings.Contains(res.Reply, "voice off") {
		t.Fatalf("off = %q", res.Reply)
	}
	if res := c.Dispatch(ctx, owner, "/voice on"); !strings.Contains(res.Reply, "voice on") {
		t.Fatalf("on = %q", res.Reply)
	}
	if settings.chat["telegram:c1.voice"] != "true" {
		t.Fatalf("toggle not saved: %v", settings.chat)
	}
	if res := c.Dispatch(ctx, owner, "/voice tts halo dunia"); !strings.Contains(res.Reply, "voice tts: audio/wav") {
		t.Fatalf("tts = %q", res.Reply)
	}
	if res := c.Dispatch(ctx, owner, "/voice tts"); !strings.Contains(res.Reply, "usage: /voice tts <text>") {
		t.Fatalf("tts usage = %q", res.Reply)
	}
	_, spoken := fv.snap()
	if len(spoken) != 1 || spoken[0] != "halo dunia" {
		t.Fatalf("spoken = %q", spoken)
	}

	fv.block = true
	checkOffCancels(ctx, t, c, fv, owner)

	broken := &fakeVoice{st: gateway.VoiceStatus{TTSReason: "module_unavailable: no tts_backend configured"}}
	c2 := gateway.NewCommands(&gateway.CommandOptions{Voice: broken})
	if res := c2.Dispatch(ctx, owner, "/voice on"); !strings.Contains(res.Reply, "voice refused:") || !strings.Contains(res.Reply, "module_unavailable") {
		t.Fatalf("unavailable on = %q", res.Reply)
	}
	if broken.st.Enabled {
		t.Fatal("voice on without a backend")
	}
	if res := c2.Dispatch(ctx, testTicket(gateway.RoleUser), "/voice on"); !strings.Contains(res.Reply, "owner-only") {
		t.Fatalf("role gate = %q", res.Reply)
	}
	nilVoice := gateway.NewCommands(&gateway.CommandOptions{})
	if res := nilVoice.Dispatch(ctx, owner, "/voice status"); !strings.Contains(res.Reply, "module_unavailable") {
		t.Fatalf("nil voice = %q", res.Reply)
	}
}

// TestF6SLASH064Wake covers /wake on|off|status, the install message when the
// engine is absent, and that a refused opt-in never claims the mic is open.
func TestF6SLASH064Wake(t *testing.T) {
	fv := &fakeVoice{st: gateway.VoiceStatus{
		Enabled: true, WakeEngine: "porcupine",
		WakeReason: "voice/wake: engine binary is not installed; install it manually",
	}}
	settings := &fakeSettings{}
	c := gateway.NewCommands(&gateway.CommandOptions{Voice: fv, Settings: settings})
	owner := testTicket(gateway.RoleOwner)
	ctx := context.Background()

	if res := c.Dispatch(ctx, owner, "/wake status"); !strings.Contains(res.Reply, "wake porcupine off") || !strings.Contains(res.Reply, "not installed") {
		t.Fatalf("status = %q", res.Reply)
	}
	res := c.Dispatch(ctx, owner, "/wake on")
	if !strings.Contains(res.Reply, "wake refused:") || !strings.Contains(res.Reply, "microphone is not open") {
		t.Fatalf("missing engine = %q", res.Reply)
	}
	if fv.st.Wake {
		t.Fatal("wake on without an installed engine")
	}
	if _, ok := settings.chat["telegram:c1.wake"]; ok {
		t.Fatal("refused wake opt-in was saved")
	}

	fv.mu.Lock()
	fv.st.WakeReason = ""
	fv.mu.Unlock()
	if res := c.Dispatch(ctx, owner, "/wake on"); !strings.Contains(res.Reply, "wake porcupine on") {
		t.Fatalf("on = %q", res.Reply)
	}
	if settings.chat["telegram:c1.wake"] != "true" {
		t.Fatalf("wake toggle not saved: %v", settings.chat)
	}
	if res := c.Dispatch(ctx, owner, "/wake off"); !strings.Contains(res.Reply, "wake porcupine off") || fv.st.Wake {
		t.Fatalf("off = %q", res.Reply)
	}
	if res := c.Dispatch(ctx, owner, "/wake bogus"); !strings.Contains(res.Reply, "usage: /wake [on|off|status]") {
		t.Fatalf("usage = %q", res.Reply)
	}
}
