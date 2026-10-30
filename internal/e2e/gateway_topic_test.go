//go:build unix

package e2e

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

var topicSessionRE = regexp.MustCompile(`topic 7: session ([A-Za-z0-9_-]+)`)

// TestServeTopicBindAndOff (SLASH-003/GW-019): help shows the thread session,
// an explicit id rebinds it, an unknown id fails closed, and off detaches it.
func TestServeTopicBindAndOff(t *testing.T) {
	e := New(t)
	tg := NewFakeTG(t, FakeTGOptions{})
	p := startGatewayServe(t, e, tg)
	push := func(text string) {
		tg.PushMessage(gatewayOwner, 7, text, FakeTGUser{ID: gatewayOwner})
	}
	waitText := func(what, needle string) {
		waitFor(t, 10*time.Second, what, func() bool {
			for _, s := range tg.Sends() {
				if strings.Contains(s.Text, needle) {
					return true
				}
			}
			return false
		})
	}
	push("/topic")
	var sid string
	waitFor(t, 10*time.Second, "topic session id", func() bool {
		for _, s := range tg.Sends() {
			if m := topicSessionRE.FindStringSubmatch(s.Text); m != nil {
				sid = m[1]
				return true
			}
		}
		return false
	})
	push("/topic " + sid)
	waitText("bound reply", "bound to session "+sid)
	push("/topic nope")
	waitText("unknown session", `"nope" not found`)
	push("/topic off")
	waitText("topic off", "topic session off")
	gatewayStop(t, p)
	db := gatewayDB(t, e)
	if n := gatewayCount(t, db, `SELECT count(*) FROM gateway_conversations WHERE thread_id = '7'`); n != 0 {
		t.Fatalf("thread bindings after off = %d, want 0", n)
	}
}
