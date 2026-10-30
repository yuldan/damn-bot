//go:build unix

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

func pausedNotices(tg *FakeTG) int {
	n := 0
	sends := tg.Sends()
	for i := range sends {
		if strings.Contains(sends[i].Text, "emergency stop is on") {
			n++
		}
	}
	return n
}

// TestServeEmergencyStopParksAndReplays is OPS-037 end to end: a paused serve
// runs no turn, the update survives a restart, and resume dispatches each
// parked message exactly once.
func TestServeEmergencyStopParksAndReplays(t *testing.T) {
	e := New(t, providertest.Text("one"), providertest.Text("two"))
	tg := NewFakeTG(t, FakeTGOptions{})
	p := startGatewayServe(t, e, tg)
	waitFor(t, 10*time.Second, "a poll", func() bool { return len(tg.RequestsFor("getUpdates")) > 0 })
	if res := e.Exec("pause", "e2e", "--data-dir", e.DataDir); res.Code != 0 {
		t.Fatalf("pause: %d\n%s\n%s", res.Code, res.Stdout, res.Stderr)
	}
	tg.PushText(gatewayOwner, "first")
	waitFor(t, 10*time.Second, "the parked update", func() bool {
		return len(tg.RequestsFor("getUpdates")) > 1
	})
	waitFor(t, 10*time.Second, "the first parked notice", func() bool { return pausedNotices(tg) >= 1 })
	gatewayStop(t, p)
	db := gatewayDB(t, e)
	if n := gatewayCount(t, db, `SELECT count(*) FROM events WHERE kind = 'user_message'`); n != 0 {
		t.Fatalf("paused serve ran %d turns, want 0", n)
	}

	p2 := startGatewayServe(t, e, tg)
	waitFor(t, 10*time.Second, "restart poll", func() bool { return len(tg.RequestsFor("getUpdates")) > 2 })
	tg.PushText(gatewayOwner, "second")
	waitFor(t, 10*time.Second, "second parked update", func() bool {
		return len(tg.RequestsFor("getUpdates")) > 3
	})
	waitFor(t, 10*time.Second, "the second parked notice", func() bool { return pausedNotices(tg) >= 2 })
	gatewayStop(t, p2)
	db = gatewayDB(t, e)
	if n := gatewayCount(t, db, `SELECT count(*) FROM events WHERE kind = 'user_message'`); n != 0 {
		t.Fatalf("restarted paused serve ran %d turns, want 0", n)
	}
	if n := gatewayCount(t, db, `SELECT count(*) FROM gateway_inbound`); n != 2 {
		t.Fatalf("parked inbound rows = %d, want 2", n)
	}

	if res := e.Exec("resume", "--data-dir", e.DataDir); res.Code != 0 {
		t.Fatalf("resume: %d\n%s\n%s", res.Code, res.Stdout, res.Stderr)
	}
	p3 := startGatewayServe(t, e, tg)
	for _, want := range []string{"one", "two"} {
		if _, err := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool {
			return strings.Contains(r.Text, want)
		}, 20*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	gatewayStop(t, p3)
	db = gatewayDB(t, e)
	if n := gatewayCount(t, db, `SELECT count(*) FROM events WHERE kind = 'user_message'`); n != 2 {
		t.Fatalf("replayed turns = %d, want 2", n)
	}
}
