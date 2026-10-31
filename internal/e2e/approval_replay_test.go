//go:build unix

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestServeTelegramApprovalReplayRejected is the F4 GAP-SEC-13 check: an
// inline keyboard from a stopped process is replayed after restart. It must
// answer "request expired", execute nothing, and not consume the fresh prompt
// of the new turn; the negative control (a prompt created after the restart,
// allowed) runs exactly once.
func TestServeTelegramApprovalReplayRejected(t *testing.T) {
	call := `{"command":"printf x >> out.txt","timeout_s":10}`
	e := NewTrusted(t,
		ToolCallReply("c1", "shell", call),
		ToolCallReply("c2", "shell", call),
		providertest.Text("done"))
	tg := NewFakeTG(t, FakeTGOptions{})
	p := startServe(t, e, binPaths.main, gatewayServeEnv(e, tg), "--drain-timeout", "1s")
	tg.PushText(gatewayOwner, "first")
	stale, err := tg.WaitSend(gatewayOwner, func(r FakeTGRequest) bool { return r.Params["reply_markup"] != "" }, 15*time.Second)
	if err != nil {
		t.Fatalf("%v\nstderr:\n%s", err, p.stderr.String())
	}
	staleData := approvalData(t, &stale, "allow")
	gatewayStop(t, p)
	if _, serr := os.Stat(filepath.Join(e.Workdir, "out.txt")); !os.IsNotExist(serr) {
		t.Fatalf("pending approval executed before any decision: %v", serr)
	}

	// Restart the same data dir and replay the old button.
	before := len(tg.RequestsFor("answerCallbackQuery"))
	p2 := startServe(t, e, binPaths.main, gatewayServeEnv(e, tg), "--drain-timeout", "1s")
	pushCallback(t, tg, &stale, staleData)
	waitFor(t, 10*time.Second, "stale callback answered", func() bool {
		return len(tg.RequestsFor("answerCallbackQuery")) > before
	})
	answers := tg.RequestsFor("answerCallbackQuery")
	if got := answers[len(answers)-1]; !strings.Contains(got.Params["text"], "request expired") {
		t.Fatalf("stale replay answer = %q, want request expired", got.Params["text"])
	}
	if _, serr := os.Stat(filepath.Join(e.Workdir, "out.txt")); !os.IsNotExist(serr) {
		t.Fatalf("replayed approval executed: %v", serr)
	}

	// Control: a fresh prompt after the restart still works once.
	tg.PushText(gatewayOwner, "second")
	fresh, err := tg.WaitSend(gatewayOwner, func(r FakeTGRequest) bool {
		return r.ResultMessageID > stale.ResultMessageID && r.Params["reply_markup"] != ""
	}, 15*time.Second)
	if err != nil {
		t.Fatalf("%v\nstderr:\n%s", err, p2.stderr.String())
	}
	if fresh.ResultMessageID == stale.ResultMessageID {
		t.Fatal("fresh prompt reuses the stale message id")
	}
	pushCallback(t, tg, &fresh, approvalData(t, &fresh, "allow"))
	if _, werr := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "done" }, 15*time.Second); werr != nil {
		t.Fatalf("%v\nstderr:\n%s", werr, p2.stderr.String())
	}
	gatewayStop(t, p2)
	body, rerr := os.ReadFile(filepath.Join(e.Workdir, "out.txt"))
	if rerr != nil || string(body) != "x" {
		t.Fatalf("allowed tool = %q, %v; want exactly one execution", body, rerr)
	}
}
