//go:build unix

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

const gatewayOwner = int64(7788)

// gatewayEnv is the serve environment of every scenario: fake bot API, fake
// provider and the env bootstrap of SPEC §9.7.
func startGatewayServe(t *testing.T, e *Env, tg *FakeTG) *serveProc {
	t.Helper()
	return startServe(t, e, binPaths.main, gatewayServeEnv(e, tg))
}

// gatewayServeEnv is the fake Telegram + fake provider environment shared by
// the gateway scenarios and the F4 budget harness.
func gatewayServeEnv(e *Env, tg *FakeTG) []string {
	return []string{
		"DAMNBOT_TELEGRAM_TOKEN=" + FakeTGToken,
		fmt.Sprintf("DAMNBOT_GATEWAY_OWNER=telegram:%d", gatewayOwner),
		"DAMNBOT_TELEGRAM_BASE_URL=" + tg.URL,
		"DAMNBOT_SERVE_MODEL=" + modelRef,
		"DAMNBOT_SERVE_BASE_URL=" + e.Server.URL,
		"DAMNBOT_SERVE_API_KEY=e2e",
	}
}

// waitFor polls until ok, failing the test at the deadline.
func waitFor(t *testing.T, limit time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if ok() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", what)
		case <-tick.C:
		}
	}
}

// gatewayDB opens the stopped data dir to inspect the transcript.
func gatewayDB(t *testing.T, e *Env) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: e.DataDir, Kind: "doctor"})
	if err != nil {
		t.Fatalf("open data dir: %v", err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close data dir: %v", cerr)
		}
	})
	return db
}

func gatewayCount(t *testing.T, db *ctxdb.Store, query string) int64 {
	t.Helper()
	var n int64
	err := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		return tx.Get(query, nil, &n)
	})
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

func gatewayStop(t *testing.T, p *serveProc) {
	t.Helper()
	p.terminate()
	if code := p.waitExit(30 * time.Second); code != 0 {
		t.Fatalf("serve exit = %d\nstderr:\n%s", code, p.stderr.String())
	}
}

func TestServeTelegramUnknownSenderSilent(t *testing.T) {
	e := New(t)
	tg := NewFakeTG(t, FakeTGOptions{})
	p := startGatewayServe(t, e, tg)
	tg.PushText(4242, "hello")
	waitFor(t, 10*time.Second, "a poll", func() bool { return len(tg.RequestsFor("getUpdates")) > 0 })
	waitFor(t, time.Second, "silence", func() bool { return len(tg.Sends()) == 0 })
	gatewayStop(t, p)
	db := gatewayDB(t, e)
	if n := gatewayCount(t, db, `SELECT count(*) FROM gateway_inbound`); n != 0 {
		t.Fatalf("gateway_inbound rows = %d, want 0", n)
	}
	if n := gatewayCount(t, db, `SELECT count(*) FROM sessions WHERE origin = 'gateway'`); n != 0 {
		t.Fatalf("gateway sessions = %d, want 0", n)
	}
}

// TestServeTelegramSendAfterIdleBackoff: after the outbox poll has backed
// off to its 1 s ceiling, an enqueued delivery is still sent within 1 s
// (next_at = enqueue time, At = fake Bot API arrival).
func TestServeTelegramSendAfterIdleBackoff(t *testing.T) {
	e := New(t, providertest.Text("pong"))
	// Editing the placeholder must fail so the final text takes the outbox
	// path (serve_gateway.go enqueues on a failed final edit).
	tg := NewFakeTG(t, FakeTGOptions{Faults: map[string]FakeTGFault{
		"editMessageText": {Status: 400, Times: 100},
	}})
	p := startGatewayServe(t, e, tg)
	waitFor(t, 10*time.Second, "a poll", func() bool { return len(tg.RequestsFor("getUpdates")) > 0 })
	<-time.After(3 * time.Second) // idle backoff reaches the 1 s interval
	tg.PushText(gatewayOwner, "hello")
	waitFor(t, 10*time.Second, "reply", func() bool { return len(tg.Sends()) > 0 })
	sends := tg.Sends()
	gatewayStop(t, p)
	db := gatewayDB(t, e)
	var nextAt int64
	if err := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT next_at FROM gateway_deliveries ORDER BY id DESC LIMIT 1`, nil, &nextAt)
	}); err != nil {
		t.Fatalf("read delivery next_at: %v", err)
	}
	// This scenario has no placeholder, so the single sendMessage is the
	// queued final text; next_at is its enqueue time.
	delay := sends[len(sends)-1].At.Sub(time.UnixMilli(nextAt))
	if delay > time.Second {
		t.Fatalf("queued delivery sent %s after enqueue, want <= 1 s", delay)
	}
	if delay < -time.Second {
		t.Fatalf("queued delivery sent %s before enqueue", delay)
	}
}

func TestServeTelegramOwnerStreamsAndResume(t *testing.T) {
	e := New(t, providertest.Text("hello ", "world"), providertest.Text("second answer"))
	tg := NewFakeTG(t, FakeTGOptions{Faults: map[string]FakeTGFault{
		"sendChatAction": {Status: 429, RetryAfter: 1},
	}})
	p := startGatewayServe(t, e, tg)
	pushed := time.Now()
	tg.PushText(gatewayOwner, "hi")
	send, err := tg.WaitSend(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "…" }, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(pushed); elapsed > 200*time.Millisecond {
		t.Errorf("placeholder took %s after the inbound, want < 200 ms", elapsed)
	}
	if n := len(tg.Sends()); n != 1 {
		t.Errorf("sends = %d, want the single placeholder", n)
	}
	edit, err := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "hello world" }, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(tg.RequestsFor("sendChatAction")); n == 0 {
		t.Error("no typing action before the reply")
	}
	if edit.MessageID != send.ResultMessageID {
		t.Fatalf("edit message %d, send message %d", edit.MessageID, send.ResultMessageID)
	}
	if n := len(tg.EditsFor(edit.MessageID)); n != 1 {
		t.Fatalf("short answer produced %d edits, want the single final edit", n)
	}
	gatewayStop(t, p)
	db := gatewayDB(t, e)
	if n := gatewayCount(t, db, `SELECT count(*) FROM events WHERE kind = 'user_message'`); n != 1 {
		t.Fatalf("user_message rows = %d, want 1", n)
	}

	before := len(tg.Sends())
	p2 := startGatewayServe(t, e, tg)
	waitFor(t, time.Second, "no replay", func() bool { return len(tg.Sends()) == before })
	tg.PushText(gatewayOwner, "again")
	waitFor(t, 15*time.Second, "second placeholder", func() bool { return len(tg.Sends()) == before+1 })
	if _, err := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "second answer" }, 15*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := len(tg.Sends()); got != before+1 {
		t.Fatalf("sends = %d, want %d", got, before+1)
	}
	gatewayStop(t, p2)
	if got := len(e.Server.Requests()); got != 2 {
		t.Fatalf("provider requests = %d, want 2", got)
	}
}

// TestServeTelegramProviderErrorReplacesPlaceholder: a failed turn edits the
// instant placeholder into one error message; it must not send a second one.
func TestServeTelegramProviderErrorReplacesPlaceholder(t *testing.T) {
	e := New(t, providertest.Status(400, "invalid_request_error", ""))
	tg := NewFakeTG(t, FakeTGOptions{})
	p := startGatewayServe(t, e, tg)
	tg.PushText(gatewayOwner, "hi")
	send, err := tg.WaitSend(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "…" }, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool { return strings.Contains(r.Text, "provider error") }, 20*time.Second)
	if err != nil {
		t.Fatalf("%v\nstderr:\n%s", err, p.stderr.String())
	}
	if edit.MessageID != send.ResultMessageID {
		t.Fatalf("error edit message %d, placeholder %d", edit.MessageID, send.ResultMessageID)
	}
	if n := len(tg.Sends()); n != 1 {
		t.Fatalf("sends = %d, want only the placeholder", n)
	}
	gatewayStop(t, p)
}

// approvalData finds the callback_data of one button in the approval message.
func approvalData(t *testing.T, send *FakeTGRequest, action string) string {
	t.Helper()
	var kb struct {
		InlineKeyboard [][]struct {
			CallbackData string `json:"callback_data"`
		} `json:"inline_keyboard"`
	}
	if err := json.Unmarshal([]byte(send.Params["reply_markup"]), &kb); err != nil {
		t.Fatalf("reply_markup: %v", err)
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if strings.HasSuffix(b.CallbackData, ":"+action) {
				return b.CallbackData
			}
		}
	}
	t.Fatalf("no %s button in %q", action, send.Params["reply_markup"])
	return ""
}

// TestServeTelegramApproval runs the same tool twice: the deny decision and
// the allow decision, and a replayed allow callback still executes once.
func TestServeTelegramApproval(t *testing.T) {
	t.Run("deny", func(t *testing.T) {
		e := NewTrusted(t, ToolCallReply("c1", "shell", `{"command":"printf x >> out.txt","timeout_s":10}`), providertest.Text("done"))
		tg := NewFakeTG(t, FakeTGOptions{})
		p := startGatewayServe(t, e, tg)
		tg.PushText(gatewayOwner, "run it")
		prompt, err := tg.WaitSend(gatewayOwner, func(r FakeTGRequest) bool { return r.Params["reply_markup"] != "" }, 15*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		pushCallback(t, tg, &prompt, approvalData(t, &prompt, "deny"))
		if _, err := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "done" }, 15*time.Second); err != nil {
			t.Fatalf("%v\nstderr:\n%s", err, p.stderr.String())
		}
		gatewayStop(t, p)
		if _, err := os.Stat(filepath.Join(e.Workdir, "out.txt")); !os.IsNotExist(err) {
			t.Fatalf("denied tool executed: %v", err)
		}
	})
	t.Run("allow", func(t *testing.T) {
		e := NewTrusted(t, ToolCallReply("c1", "shell", `{"command":"printf x >> out.txt","timeout_s":10}`), providertest.Text("done"))
		tg := NewFakeTG(t, FakeTGOptions{})
		p := startGatewayServe(t, e, tg)
		tg.PushText(gatewayOwner, "run it")
		prompt, err := tg.WaitSend(gatewayOwner, func(r FakeTGRequest) bool { return r.Params["reply_markup"] != "" }, 15*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		data := approvalData(t, &prompt, "allow")
		pushCallback(t, tg, &prompt, data)
		pushCallback(t, tg, &prompt, data) // replayed callback: must stay one execution
		if _, eerr := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "done" }, 15*time.Second); eerr != nil {
			t.Fatalf("%v\nstderr:\n%s", eerr, p.stderr.String())
		}
		gatewayStop(t, p)
		body, err := os.ReadFile(filepath.Join(e.Workdir, "out.txt"))
		if err != nil {
			t.Fatalf("allowed tool did not run: %v", err)
		}
		if string(body) != "x" {
			t.Fatalf("out.txt = %q, want one execution", body)
		}
	})
}

func pushCallback(t *testing.T, tg *FakeTG, prompt *FakeTGRequest, data string) {
	t.Helper()
	msg := &FakeTGMessage{MessageID: prompt.ResultMessageID, Chat: FakeTGChat{ID: prompt.ChatID}}
	cb := &FakeTGCallbackQuery{ID: fmt.Sprintf("cb-%d", prompt.ResultMessageID), From: FakeTGUser{ID: gatewayOwner}, Data: data, Message: msg}
	tg.PushUpdate(FakeTGUpdate{CallbackQuery: cb})
}

func TestServeTelegramOfflineZeroRequests(t *testing.T) {
	e := New(t)
	if err := os.WriteFile(e.cfg, []byte("[network]\noffline = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tg := NewFakeTG(t, FakeTGOptions{})
	p := startGatewayServe(t, e, tg)
	p.awaitLog(p.stderr, "gateway polling", 10*time.Second)
	waitFor(t, 500*time.Millisecond, "offline silence", func() bool {
		return len(tg.Requests()) == 0 && len(e.Server.Requests()) == 0
	})
	gatewayStop(t, p)
	if n := len(tg.Requests()); n != 0 {
		t.Fatalf("telegram requests = %d, want 0", n)
	}
}

func TestServeTelegramSigtermDrainsTurn(t *testing.T) {
	slow := providertest.Text("slow answer")
	slow.Delay = 400 * time.Millisecond
	e := New(t, slow)
	tg := NewFakeTG(t, FakeTGOptions{})
	p := startGatewayServe(t, e, tg)
	tg.PushText(gatewayOwner, "slow")
	waitFor(t, 15*time.Second, "provider request", func() bool { return len(e.Server.Requests()) > 0 })
	gatewayStop(t, p)
	if _, err := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "slow answer" }, 10*time.Second); err != nil {
		t.Fatalf("drained reply missing: %v", err)
	}
}
