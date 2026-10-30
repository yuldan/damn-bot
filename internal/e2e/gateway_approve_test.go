//go:build unix

package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

var approvalIDRE = regexp.MustCompile(`approval: ([A-Za-z0-9_-]+)`)

// TestServeTelegramApproveCommand (SLASH-022): the text command denies or allows
// the live tool card, and a replayed approval cannot execute twice.
func TestServeTelegramApproveCommand(t *testing.T) {
	run := func(t *testing.T, verb string, wantFile bool, replay bool) {
		t.Helper()
		e := NewTrusted(t, ToolCallReply("c1", "shell", `{"command":"printf x >> out.txt","timeout_s":10}`), providertest.Text("done"))
		tg := NewFakeTG(t, FakeTGOptions{})
		p := startGatewayServe(t, e, tg)
		tg.PushText(gatewayOwner, "run it")
		prompt, err := tg.WaitSend(gatewayOwner, func(r FakeTGRequest) bool { return r.Params["reply_markup"] != "" }, 15*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		m := approvalIDRE.FindStringSubmatch(prompt.Text)
		if m == nil {
			t.Fatalf("approval card has no id: %q", prompt.Text)
		}
		tg.PushText(gatewayOwner, "/"+verb+" "+m[1])
		if replay {
			tg.PushText(gatewayOwner, "/"+verb+" "+m[1])
		}
		if _, werr := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "done" }, 15*time.Second); werr != nil {
			t.Fatalf("%v\nstderr:\n%s", werr, p.stderr.String())
		}
		gatewayStop(t, p)
		body, rerr := os.ReadFile(filepath.Join(e.Workdir, "out.txt"))
		if wantFile {
			if rerr != nil || string(body) != "x" {
				t.Fatalf("out.txt = %q err=%v, want one execution", body, rerr)
			}
			return
		}
		if !os.IsNotExist(rerr) {
			t.Fatalf("denied tool executed: %v %q", rerr, body)
		}
	}
	t.Run("deny", func(t *testing.T) { run(t, "deny", false, false) })
	t.Run("allow", func(t *testing.T) { run(t, "approve", true, false) })
	t.Run("replay", func(t *testing.T) {
		run(t, "approve", true, true)
		if testing.Short() {
			return
		}
	})
}

// TestApproveWithoutPending: a text approval with an unknown id never executes
// a tool and says so.
func TestApproveWithoutPending(t *testing.T) {
	e := New(t)
	tg := NewFakeTG(t, FakeTGOptions{})
	startGatewayServe(t, e, tg)
	tg.PushText(gatewayOwner, "/approve nope")
	waitFor(t, 10*time.Second, "approval not pending", func() bool {
		for _, s := range tg.Sends() {
			if strings.Contains(s.Text, "not found") || strings.Contains(s.Text, "no longer pending") {
				return true
			}
		}
		return false
	})
}
