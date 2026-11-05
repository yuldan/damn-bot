//go:build unix

package e2e

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

var approvalIDRE = regexp.MustCompile(`approval: ([A-Za-z0-9_-]+)`)

// TestServeTelegramApproveCommand (SLASH-022): the text command denies or allows
// the live tool card, and a replayed approval cannot execute twice.
func TestServeTelegramApproveCommand(t *testing.T) {
	run := func(t *testing.T, verb string, wantFile, replay bool) {
		t.Helper()
		e := NewTrusted(t, ToolCallReply("c1", "shell", `{"command":"printf x >> out.txt","timeout_s":10}`), providertest.Text("done"))
		tg := NewFakeTG(t, FakeTGOptions{})
		p := startGatewayServe(t, e, tg)
		id := openApprovalCard(t, tg)
		tg.PushText(gatewayOwner, "/"+verb+" "+id)
		if replay {
			tg.PushText(gatewayOwner, "/"+verb+" "+id)
		}
		awaitAnswer(t, tg, p, verb, id)
		gatewayStop(t, p)
		assertExecutions(t, e, wantFile)
	}
	t.Run("deny", func(t *testing.T) { run(t, "deny", false, false) })
	t.Run("allow", func(t *testing.T) { run(t, "approve", true, false) })
	t.Run("replay", func(t *testing.T) { run(t, "approve", true, true) })
}

// openApprovalCard prompts the gateway with one tool call and returns the
// approval id its card carries.
func openApprovalCard(t *testing.T, tg *FakeTG) string {
	t.Helper()
	tg.PushText(gatewayOwner, "run it")
	prompt, err := tg.WaitSend(gatewayOwner, func(r FakeTGRequest) bool { return r.Params["reply_markup"] != "" }, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	m := approvalIDRE.FindStringSubmatch(prompt.Text)
	if m == nil {
		t.Fatalf("approval card has no id: %q", prompt.Text)
	}
	return m[1]
}

// awaitAnswer waits for the turn's answer edit and fails with the gateway's own
// reply when the command did not resolve the card it named. WaitEdit wakes on
// the fake's request signal, so the answer is checked the moment it is
// recorded instead of on a polling tick.
func awaitAnswer(t *testing.T, tg *FakeTG, p *serveProc, verb, id string) {
	t.Helper()
	edit, werr := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "done" }, 15*time.Second)
	if werr != nil {
		t.Fatalf("%v\nstderr:\n%s", werr, p.stderr.String())
	}
	if edit.ChatID != gatewayOwner || edit.MessageID == 0 {
		t.Fatalf("answer edit = %+v, want chat %d on a message id", edit, gatewayOwner)
	}
	sends := tg.Sends()
	for i := range sends {
		if unresolvedApproval(sends[i].Text) {
			t.Fatalf("/%s %s was not resolved: gateway replied %q", verb, id, sends[i].Text)
		}
	}
}

// assertExecutions checks the one-shot shell side effect of the turn: one line
// when the tool was allowed, no file when it was denied.
func assertExecutions(t *testing.T, e *Env, wantFile bool) {
	t.Helper()
	body, rerr := os.ReadFile(filepath.Join(e.Workdir, "out.txt"))
	if !wantFile {
		if !os.IsNotExist(rerr) {
			t.Fatalf("denied tool executed: %v %q", rerr, body)
		}
		return
	}
	if rerr != nil || string(body) != "x" {
		t.Fatalf("out.txt = %q err=%v, want one execution", body, rerr)
	}
}

// TestServeTelegramApproveAnsweredOnArrival pins the ordering between the card
// and the registry: the card is answerable the instant its send is recorded,
// before the gateway has even seen the response, so the approval must be
// registered before the card goes out. When it is not, /deny misses the
// registry, the answer is dropped, and the turn waits for the approval timeout
// instead of finishing. The send response is held back so the unregistered
// window is wider than any scheduling jitter.
func TestServeTelegramApproveAnsweredOnArrival(t *testing.T) {
	e := NewTrusted(t, ToolCallReply("c1", "shell", `{"command":"printf x >> out.txt","timeout_s":10}`), providertest.Text("done"))
	tg := NewFakeTG(t, FakeTGOptions{Delays: map[string]time.Duration{"sendMessage": 400 * time.Millisecond}})
	var once sync.Once
	tg.OnCall(func(r FakeTGRequest) {
		if r.Method != "sendMessage" || r.Params["reply_markup"] == "" {
			return
		}
		if m := approvalIDRE.FindStringSubmatch(r.Text); m != nil {
			once.Do(func() { tg.PushText(gatewayOwner, "/deny "+m[1]) })
		}
	})
	p := startGatewayServe(t, e, tg)
	tg.PushText(gatewayOwner, "run it")
	if _, werr := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "done" }, 15*time.Second); werr != nil {
		t.Fatalf("%v\nstderr:\n%s", werr, p.stderr.String())
	}
	gatewayStop(t, p)
	if body, rerr := os.ReadFile(filepath.Join(e.Workdir, "out.txt")); !os.IsNotExist(rerr) {
		t.Fatalf("denied tool executed: %v %q", rerr, body)
	}
	for _, s := range tg.Sends() {
		if unresolvedApproval(s.Text) {
			t.Fatalf("answer sent with the card was not resolved: gateway replied %q", s.Text)
		}
	}
}

// unresolvedApproval reports a command reply that refused to resolve the id it
// was given. "Already approved/denied" is not one of them: replaying an answer
// is idempotent on purpose.
func unresolvedApproval(text string) bool {
	for _, refusal := range []string{"not found in this chat", "is no longer open", "is no longer pending", "could not be resolved"} {
		if strings.Contains(text, refusal) {
			return true
		}
	}
	return false
}

// TestWaitEditWithoutEditFailsClearly is the negative control of the wait the
// scenario above relies on: with no matching edit the wait returns a bounded,
// explanatory error instead of blocking to some outer deadline, and it names
// the edits it did see so a failed run shows what the gateway sent.
func TestWaitEditWithoutEditFailsClearly(t *testing.T) {
	tg := NewFakeTG(t, FakeTGOptions{})
	form := url.Values{
		"chat_id":    {strconv.FormatInt(gatewayOwner, 10)},
		"message_id": {"1"},
		"text":       {"still working"},
	}
	if status, body, err := tg.Call(t.Context(), "editMessageText", form); err != nil || status != http.StatusOK {
		t.Fatalf("seed edit: status=%d body=%s err=%v", status, body, err)
	}
	const wait = 200 * time.Millisecond
	started := time.Now()
	_, err := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "never" }, wait)
	if err == nil {
		t.Fatal("WaitEdit matched an edit that was never sent")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("WaitEdit returned after %s, want the %s deadline", elapsed, wait)
	}
	for _, want := range []string{"no matching edit to chat 7788", "within 200ms", "1 editMessageText", `"still working"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not report %q", err, want)
		}
	}
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
