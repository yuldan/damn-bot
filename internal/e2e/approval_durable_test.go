//go:build unix

package e2e

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// approvalPrompts counts approval cards in sends[from:].
func approvalPrompts(sends []FakeTGRequest, from int) int {
	n := 0
	for i := from; i < len(sends); i++ {
		if sends[i].Params["reply_markup"] != "" {
			n++
		}
	}
	return n
}

// TestServeTelegramApprovalDurableApprove is GAP-SEC-13 replay: an approved
// request is recorded and marked executed. After kill -9 and restart, the same
// session/prompt/tool fingerprint is not prompted again and the tool is not
// executed a second time.
func TestServeTelegramApprovalDurableApprove(t *testing.T) {
	call := `{"command":"printf x >> out.txt","timeout_s":10}`
	e := NewTrusted(t,
		ToolCallReply("c1", "shell", call),
		providertest.Text("done1"),
		providertest.Text("done2"))
	tg := NewFakeTG(t, FakeTGOptions{})
	p := startGatewayServe(t, e, tg)
	tg.PushText(gatewayOwner, "run it")
	prompt, err := tg.WaitSend(gatewayOwner, func(r FakeTGRequest) bool { return r.Params["reply_markup"] != "" }, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pushCallback(t, tg, &prompt, approvalData(t, &prompt, "allow"))
	if _, werr := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "done1" }, 15*time.Second); werr != nil {
		t.Fatalf("%v\nstderr:\n%s", werr, p.stderr.String())
	}
	body, rerr := os.ReadFile(filepath.Join(e.Workdir, "out.txt"))
	if rerr != nil || string(body) != "x" {
		t.Fatalf("first execution = %q, %v", body, rerr)
	}
	if kerr := p.cmd.Process.Kill(); kerr != nil {
		t.Fatal(kerr)
	}
	_ = p.waitExit(10 * time.Second)

	before := len(tg.Sends())
	p2 := startGatewayServe(t, e, tg)
	tg.PushText(gatewayOwner, "run it")
	if _, werr := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "done2" }, 15*time.Second); werr != nil {
		t.Fatalf("%v\nstderr:\n%s", werr, p2.stderr.String())
	}
	gatewayStop(t, p2)
	if got := approvalPrompts(tg.Sends(), before); got != 0 {
		t.Fatalf("re-issued prompt after restart = %d, want 0", got)
	}
	body, rerr = os.ReadFile(filepath.Join(e.Workdir, "out.txt"))
	if rerr != nil || string(body) != "x" {
		t.Fatalf("tool executed twice: %q, %v", body, rerr)
	}
	db := gatewayDB(t, e)
	if n := gatewayCount(t, db, `SELECT count(*) FROM module_state WHERE module_id = 'approval'`); n != 1 {
		t.Fatalf("approval records = %d, want 1", n)
	}
}

// TestServeTelegramApprovalDurableDeny: a denied request stays denied across
// a restart, with no new keyboard and no execution.
func TestServeTelegramApprovalDurableDeny(t *testing.T) {
	call := `{"command":"printf x >> out.txt","timeout_s":10}`
	e := NewTrusted(t,
		ToolCallReply("c1", "shell", call),
		providertest.Text("done1"),
		providertest.Text("done2"))
	tg := NewFakeTG(t, FakeTGOptions{})
	p := startGatewayServe(t, e, tg)
	tg.PushText(gatewayOwner, "run it")
	prompt, err := tg.WaitSend(gatewayOwner, func(r FakeTGRequest) bool { return r.Params["reply_markup"] != "" }, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pushCallback(t, tg, &prompt, approvalData(t, &prompt, "deny"))
	if _, werr := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "done1" }, 15*time.Second); werr != nil {
		t.Fatalf("%v\nstderr:\n%s", werr, p.stderr.String())
	}
	gatewayStop(t, p)

	before := len(tg.Sends())
	p2 := startGatewayServe(t, e, tg)
	tg.PushText(gatewayOwner, "run it")
	if _, werr := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "done2" }, 15*time.Second); werr != nil {
		t.Fatalf("%v\nstderr:\n%s", werr, p2.stderr.String())
	}
	gatewayStop(t, p2)
	if got := approvalPrompts(tg.Sends(), before); got != 0 {
		t.Fatalf("deny was re-prompted after restart: %d cards", got)
	}
	if _, serr := os.Stat(filepath.Join(e.Workdir, "out.txt")); !os.IsNotExist(serr) {
		t.Fatalf("denied tool executed after restart: %v", serr)
	}
}
