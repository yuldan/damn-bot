//go:build unix

package e2e

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
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
	<-time.After(300 * time.Millisecond)
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
	<-time.After(500 * time.Millisecond)
	if n := len(tg.RequestsFor("sendMessage")); n != 0 {
		t.Fatalf("offline sendMessage requests = %d, want 0", n)
	}
}
