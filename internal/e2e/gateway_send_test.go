//go:build unix

package e2e

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// sendEnv is the environment the send CLI needs to talk to the fake Bot API.
func sendEnv(tg *FakeTG) []string {
	return []string{
		"DAMNBOT_TELEGRAM_TOKEN=" + FakeTGToken,
		"DAMNBOT_TELEGRAM_BASE_URL=" + tg.URL,
	}
}

// TestServeSendCLIEnqueuesOnce: CLI-018/GW-010 posts through the running
// serve's control seam, the outbox records one row, and replaying the identical
// command never sends a second Telegram message.
func TestServeSendCLIEnqueuesOnce(t *testing.T) {
	e := New(t)
	tg := NewFakeTG(t, FakeTGOptions{})
	startGatewayServe(t, e, tg)
	args := []string{
		"send", "--platform", "telegram", "--chat", strconv.FormatInt(gatewayOwner, 10),
		"--text", "hello from ci", "--data-dir", e.DataDir,
	}
	res := e.ExecEnv(sendEnv(tg), args...)
	if res.Code != 0 || !strings.Contains(res.Stdout, "queued") {
		t.Fatalf("send: code=%d stdout=%q stderr=%q", res.Code, res.Stdout, res.Stderr)
	}
	waitFor(t, 5*time.Second, "one Telegram send", func() bool { return len(tg.Sends()) == 1 })
	again := e.ExecEnv(sendEnv(tg), args...)
	if again.Code != 0 || !strings.Contains(again.Stdout, "queued") {
		t.Fatalf("replay: code=%d stdout=%q stderr=%q", again.Code, again.Stdout, again.Stderr)
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(tg.Sends()); n != 1 {
		t.Fatalf("Telegram sends = %d, want exactly 1 (replay must not duplicate)", n)
	}
}

// TestServeSendCLIOfflineNoRequest: [network] offline enqueues the row but
// performs no outbound request at all.
func TestServeSendCLIOfflineNoRequest(t *testing.T) {
	e := New(t)
	if err := os.WriteFile(e.cfg, []byte("[network]\noffline = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tg := NewFakeTG(t, FakeTGOptions{})
	startGatewayServe(t, e, tg)
	res := e.ExecEnv(sendEnv(tg), "send", "--platform", "telegram", "--chat",
		strconv.FormatInt(gatewayOwner, 10), "--text", "queued only", "--data-dir", e.DataDir)
	if res.Code != 0 || !strings.Contains(res.Stdout, "queued") {
		t.Fatalf("offline send: code=%d stdout=%q stderr=%q", res.Code, res.Stdout, res.Stderr)
	}
	time.Sleep(500 * time.Millisecond)
	if n := len(tg.RequestsFor("sendMessage")); n != 0 {
		t.Fatalf("offline sendMessage requests = %d, want 0", n)
	}
}

// TestServePlatformPauseResume: pausing stops the adapter (no turn, no provider
// request); resuming reloads the persisted offset, so exactly the message that
// arrived while paused is processed once and never replayed.
func TestServePlatformPauseResume(t *testing.T) {
	e := New(t, providertest.Text("one"), providertest.Text("two"))
	tg := NewFakeTG(t, FakeTGOptions{})
	startGatewayServe(t, e, tg)
	pause := e.ExecEnv(sendEnv(tg), "gateway", "platform", "pause", "telegram", "--data-dir", e.DataDir)
	if pause.Code != 0 || !strings.Contains(pause.Stdout, "requested") {
		t.Fatalf("pause: code=%d stdout=%q stderr=%q", pause.Code, pause.Stdout, pause.Stderr)
	}
	waitFor(t, 5*time.Second, "the paused snapshot", func() bool {
		pc := e.ExecEnv(sendEnv(tg), "gateway", "platform", "list", "--data-dir", e.DataDir)
		return strings.Contains(pc.Stdout, "paused")
	})
	before := len(e.Server.Requests())
	tg.PushText(gatewayOwner, "while paused")
	time.Sleep(700 * time.Millisecond)
	if got := len(e.Server.Requests()); got != before {
		t.Fatalf("provider requests while paused = %d, want %d (0 turns)", got, before)
	}
	resume := e.ExecEnv(sendEnv(tg), "gateway", "platform", "resume", "telegram", "--data-dir", e.DataDir)
	if resume.Code != 0 || !strings.Contains(resume.Stdout, "requested") {
		t.Fatalf("resume: code=%d stdout=%q stderr=%q", resume.Code, resume.Stdout, resume.Stderr)
	}
	waitFor(t, 5*time.Second, "the running snapshot", func() bool {
		pc := e.ExecEnv(sendEnv(tg), "gateway", "platform", "list", "--data-dir", e.DataDir)
		return strings.Contains(pc.Stdout, "running")
	})
	waitFor(t, 10*time.Second, "the message queued while paused", func() bool {
		return len(e.Server.Requests()) == before+1
	})
	time.Sleep(500 * time.Millisecond)
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
