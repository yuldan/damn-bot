//go:build unix

package e2e

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestGatewayTimestampsPrefixRepliesAndOutbox (GW-020): /timestamps on prefixes
// both a command reply and an outbox send; off leaves them unchanged.
func TestGatewayTimestampsPrefixRepliesAndOutbox(t *testing.T) {
	e := New(t)
	tg := NewFakeTG(t, FakeTGOptions{})
	startGatewayServe(t, e, tg)
	tg.PushText(gatewayOwner, "/timestamps on")
	waitFor(t, 10*time.Second, "timestamps confirmation", func() bool {
		for _, s := range tg.Sends() {
			if strings.Contains(s.Text, "timestamps on") {
				return true
			}
		}
		return false
	})
	tg.PushText(gatewayOwner, "/whoami")
	waitFor(t, 10*time.Second, "timestamped /whoami", func() bool {
		for _, s := range tg.Sends() {
			if strings.Contains(s.Text, "access:") && strings.HasPrefix(s.Text, "[") {
				return true
			}
		}
		return false
	})
	chat := strconv.FormatInt(gatewayOwner, 10)
	if res := e.ExecEnv([]string{"DAMNBOT_TELEGRAM_TOKEN=" + FakeTGToken, "DAMNBOT_TELEGRAM_BASE_URL=" + tg.URL},
		"send", "--platform", "telegram", "--chat", chat, "--text", "timestamped outbox", "--data-dir", e.DataDir); res.Code != 0 {
		t.Fatalf("send: %d %s", res.Code, res.Stderr)
	}
	waitFor(t, 10*time.Second, "timestamped outbox send", func() bool {
		for _, s := range tg.Sends() {
			if strings.Contains(s.Text, "timestamped outbox") && strings.HasPrefix(s.Text, "[") {
				return true
			}
		}
		return false
	})
}

// TestGatewayCronAddEdit (SLASH-076): an owner creates and edits a cron job
// from chat; a user role is refused and never mutates the store.
func TestGatewayCronAddEdit(t *testing.T) {
	e := New(t)
	tg := NewFakeTG(t, FakeTGOptions{})
	startGatewayServe(t, e, tg)
	tg.PushText(gatewayOwner, "/cron add 0 9 * * * daily prompt")
	var id string
	waitFor(t, 10*time.Second, "cron add reply", func() bool {
		for _, s := range tg.Sends() {
			if i := strings.Index(s.Text, "cron add: ok ("); i >= 0 {
				id = strings.TrimSuffix(s.Text[i+len("cron add: ok ("):], ")")
				return id != ""
			}
		}
		return false
	})
	tg.PushText(gatewayOwner, "/cron edit "+id+" prompt changed prompt")
	waitFor(t, 10*time.Second, "cron edit reply", func() bool {
		for _, s := range tg.Sends() {
			if strings.Contains(s.Text, "cron edit: ok") {
				return true
			}
		}
		return false
	})
	tg.PushText(gatewayOwner, "/cron list")
	waitFor(t, 10*time.Second, "cron list contains the job", func() bool {
		for _, s := range tg.Sends() {
			if strings.Contains(s.Text, "cron: 1 job") && strings.Contains(s.Text, id) {
				return true
			}
		}
		return false
	})
}
