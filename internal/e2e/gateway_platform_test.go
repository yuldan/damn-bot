//go:build unix

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// gatewayEnv is the environment the control CLI needs to reach the fake API.
func gatewayEnv(tg *FakeTG) []string {
	return []string{
		"DAMNBOT_TELEGRAM_TOKEN=" + FakeTGToken,
		"DAMNBOT_TELEGRAM_BASE_URL=" + tg.URL,
	}
}

// TestServePlatformPauseResume: pausing stops the adapter (no turn, no provider
// request); resuming reloads the persisted offset, so exactly the message that
// arrived while paused is processed once and never replayed.
func TestServePlatformPauseResume(t *testing.T) {
	e := New(t, providertest.Text("one"), providertest.Text("two"))
	tg := NewFakeTG(t, FakeTGOptions{})
	startGatewayServe(t, e, tg)
	pause := e.ExecEnv(gatewayEnv(tg), "gateway", "platform", "pause", "telegram", "--data-dir", e.DataDir)
	if pause.Code != 0 || !strings.Contains(pause.Stdout, "requested") {
		t.Fatalf("pause: code=%d stdout=%q stderr=%q", pause.Code, pause.Stdout, pause.Stderr)
	}
	waitFor(t, 5*time.Second, "the paused snapshot", func() bool {
		pc := e.ExecEnv(gatewayEnv(tg), "gateway", "platform", "list", "--data-dir", e.DataDir)
		return strings.Contains(pc.Stdout, "paused")
	})
	before := len(e.Server.Requests())
	tg.PushText(gatewayOwner, "while paused")
	<-time.After(700 * time.Millisecond)
	if got := len(e.Server.Requests()); got != before {
		t.Fatalf("provider requests while paused = %d, want %d (0 turns)", got, before)
	}
	resume := e.ExecEnv(gatewayEnv(tg), "gateway", "platform", "resume", "telegram", "--data-dir", e.DataDir)
	if resume.Code != 0 || !strings.Contains(resume.Stdout, "requested") {
		t.Fatalf("resume: code=%d stdout=%q stderr=%q", resume.Code, resume.Stdout, resume.Stderr)
	}
	waitFor(t, 5*time.Second, "the running snapshot", func() bool {
		pc := e.ExecEnv(gatewayEnv(tg), "gateway", "platform", "list", "--data-dir", e.DataDir)
		return strings.Contains(pc.Stdout, "running")
	})
	waitFor(t, 10*time.Second, "the message queued while paused", func() bool {
		return len(e.Server.Requests()) == before+1
	})
	<-time.After(500 * time.Millisecond)
	if got := len(e.Server.Requests()); got != before+1 {
		t.Fatalf("provider requests after resume = %d, want %d (the offset must not replay)", got, before+1)
	}
}

// TestServeRestartWaitsForTurn: /restart answers first, does not cut the active
// turn, and only then leaves with the planned-restart exit code 75.
func TestServeRestartWaitsForTurn(t *testing.T) {
	slow := providertest.Text("slow answer")
	slow.Delay = time.Second
	e := New(t, slow)
	tg := NewFakeTG(t, FakeTGOptions{})
	p := startGatewayServe(t, e, tg)
	tg.PushText(gatewayOwner, "long turn")
	waitFor(t, 10*time.Second, "the turn to reach the provider", func() bool {
		return len(e.Server.Requests()) == 1
	})
	tg.PushText(gatewayOwner, "/restart")
	waitFor(t, 10*time.Second, "the restart reply", func() bool {
		for _, s := range tg.Sends() {
			if strings.Contains(s.Text, "restart scheduled") {
				return true
			}
		}
		return false
	})
	select {
	case <-p.exited:
		t.Fatalf("serve exited before the active turn finished\nstderr:\n%s", p.stderr.String())
	default:
	}
	if code := p.waitExit(20 * time.Second); code != 75 { // EX_TEMPFAIL: planned restart
		t.Fatalf("serve restart exit = %d, want 75\nstderr:\n%s", code, p.stderr.String())
	}
}
