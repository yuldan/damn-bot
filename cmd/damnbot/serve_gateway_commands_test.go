package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/telegram"
)

// commandSends is a fake Bot API that records the text of every sendMessage.
type commandSends struct {
	url  string
	mu   sync.Mutex
	got  []string
	note chan struct{}
}

func newCommandSends(t *testing.T) *commandSends {
	t.Helper()
	cs := &commandSends{note: make(chan struct{}, 64)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, `{"ok":false}`, http.StatusBadRequest)
			return
		}
		cs.mu.Lock()
		cs.got = append(cs.got, r.PostForm.Get("text"))
		cs.mu.Unlock()
		cs.note <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`); err != nil {
			return
		}
	}))
	t.Cleanup(srv.Close)
	cs.url = srv.URL
	return cs
}

// wait returns the first recorded text containing sub, or fails after 5 s.
func (c *commandSends) wait(t *testing.T, sub string) string {
	t.Helper()
	return c.waitN(t, sub, 1)[0]
}

// waitN waits for n recorded texts containing sub. It blocks on the send
// signal instead of polling.
func (c *commandSends) waitN(t *testing.T, sub string, n int) []string {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		c.mu.Lock()
		var matched []string
		for _, text := range c.got {
			if strings.Contains(text, sub) {
				matched = append(matched, text)
			}
		}
		c.mu.Unlock()
		if len(matched) >= n {
			return matched
		}
		select {
		case <-c.note:
		case <-timeout.C:
			t.Fatalf("no %d replies containing %q; sent: %v", n, sub, c.texts())
		}
	}
}

func (c *commandSends) texts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.got)
}

// newCommandGateway builds the serve gateway over a fake Bot API with no token
// of its own: every command reply is a sendMessage the recorder sees.
func newCommandGateway(t *testing.T) (*serveGateway, *commandSends) {
	t.Helper()
	sends := newCommandSends(t)
	gw := newTestGateway(t, sends.url)
	t.Cleanup(func() { gw.Stop(context.Background()) })
	return gw, sends
}

// allowGatewayUser seeds and allowlists one role user identity.
func allowGatewayUser(t *testing.T, gw *serveGateway, id string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if _, err := gw.store.EnsureIdentity(t.Context(), "telegram", id, "user", "platform_authenticated", now); err != nil {
		t.Fatal(err)
	}
	if err := gw.store.SetStatus(t.Context(), "telegram", id, "allowlisted", now); err != nil {
		t.Fatal(err)
	}
}

func cmdInbound(chat, sender, update, text string) *telegram.Inbound {
	return &telegram.Inbound{Platform: "telegram", ChatID: chat, SenderID: sender, UpdateID: update, Text: text}
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestGatewayStopCancelsOnlyThatChat: two chats run turns; /stop in one cancels
// that turn and leaves the other running. The reply is the whole command
// output (no turn is started for it).
func TestGatewayStopCancelsOnlyThatChat(t *testing.T) {
	gw, sends := newCommandGateway(t)
	allowGatewayUser(t, gw, "9001")
	startedA, startedB := make(chan struct{}), make(chan struct{})
	exitedB := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	gw.run = func(ctx context.Context, in *telegram.Inbound, _ *gateway.Ticket) error {
		switch in.ChatID {
		case "100":
			close(startedA)
		case "200":
			close(startedB)
			defer close(exitedB)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	}
	gw.dispatch(t.Context(), cmdInbound("100", "7788", "u:1", "halo"))
	gw.dispatch(t.Context(), cmdInbound("200", "9001", "u:2", "halo"))
	waitClosed(t, startedA, "chat 100 turn")
	waitClosed(t, startedB, "chat 200 turn")

	gw.dispatch(t.Context(), cmdInbound("100", "7788", "u:3", "/stop"))
	if reply := sends.wait(t, "turn cancelled"); reply != "turn cancelled" {
		t.Fatalf("stop reply = %q", reply)
	}
	select {
	case <-exitedB:
		t.Fatal("/stop in chat 100 cancelled chat 200's turn")
	case <-time.After(200 * time.Millisecond):
	}
	gw.dispatch(t.Context(), cmdInbound("100", "7788", "u:4", "/stop"))
	sends.wait(t, "no active turn in this chat")
}

// TestGatewayStopTwiceCancelsOnce: while the cancelled turn is still unwinding,
// the second /stop must not cancel again — the turn reports a single
// cancellation and the second command answers "no active turn".
func TestGatewayStopTwiceCancelsOnce(t *testing.T) {
	gw, sends := newCommandGateway(t)
	started := make(chan struct{})
	cancelled := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	gw.run = func(ctx context.Context, _ *telegram.Inbound, _ *gateway.Ticket) error {
		close(started)
		<-ctx.Done()
		cancelled <- struct{}{}
		<-release
		return ctx.Err()
	}
	gw.dispatch(t.Context(), cmdInbound("150", "7788", "u:1", "halo"))
	waitClosed(t, started, "turn start")

	gw.dispatch(t.Context(), cmdInbound("150", "7788", "u:2", "/stop"))
	sends.wait(t, "turn cancelled")
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("the runner never observed the cancellation")
	}
	gw.dispatch(t.Context(), cmdInbound("150", "7788", "u:3", "/stop"))
	sends.wait(t, "no active turn in this chat")
	select {
	case <-cancelled:
		t.Fatal("the second /stop cancelled the turn again")
	default:
	}
}

// TestGatewayNewKeepsPreviousSession: /new binds a second session to the route
// and the first session plus its binding stay stored.
func TestGatewayNewKeepsPreviousSession(t *testing.T) {
	gw, sends := newCommandGateway(t)
	gw.dispatch(t.Context(), cmdInbound("300", "7788", "u:1", "/new"))
	sends.wait(t, "started a new session")
	gw.dispatch(t.Context(), cmdInbound("300", "7788", "u:2", "/new"))
	sends.waitN(t, "started a new session", 2)

	ctx := t.Context()
	var ids []string
	var count int64
	err := gw.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		var xerr error
		ids, xerr = tx.Strings(`SELECT session_id FROM gateway_conversations
			WHERE platform = 'telegram' AND chat_id = '300' ORDER BY created_at DESC, rowid DESC`)
		if xerr != nil {
			return fmt.Errorf("read bindings: %w", xerr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("session bindings = %v, want two distinct sessions", ids)
	}
	if err := gw.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT COUNT(*) FROM sessions WHERE id IN (?1, ?2)`, []any{ids[0], ids[1]}, &count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("stored sessions = %d, want both kept", count)
	}
	if got, _, serr := gw.store.Session(ctx, gwstoreRoute("300"), "telegram:7788"); serr != nil || got != ids[0] {
		t.Fatalf("current session = %q (err %v), want the newest %q", got, serr, ids[0])
	}
}

// TestGatewayCommandsNeverRunTheRunner: /status, /usage and an unknown command
// answer from state only; the turn runner (and therefore the LLM) is never
// called, and a user's /status is refused before the runtime.
func TestGatewayCommandsNeverRunTheRunner(t *testing.T) {
	gw, sends := newCommandGateway(t)
	var mu sync.Mutex
	calls := 0
	gw.run = func(context.Context, *telegram.Inbound, *gateway.Ticket) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil
	}
	gw.dispatch(t.Context(), cmdInbound("400", "7788", "u:1", "/status"))
	status := sends.wait(t, "status:")
	for _, want := range []string{"queue", "active turns", "mode normal", "version"} {
		if !strings.Contains(status, want) {
			t.Errorf("status reply %q lacks %q", status, want)
		}
	}
	gw.dispatch(t.Context(), cmdInbound("401", "7788", "u:2", "/usage"))
	sends.wait(t, "no usage yet")
	gw.dispatch(t.Context(), cmdInbound("402", "7788", "u:3", "/nope"))
	sends.wait(t, "unknown command /nope")

	allowGatewayUser(t, gw, "9001")
	gw.dispatch(t.Context(), cmdInbound("500", "9001", "u:4", "/status"))
	sends.wait(t, "owner-only")
	gw.dispatch(t.Context(), cmdInbound("501", "9001", "u:5", "/usage"))
	sends.wait(t, "owner-only")

	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("runner calls = %d, want 0 for commands", calls)
	}
}

func gwstoreRoute(chat string) gwstore.Route {
	return gwstore.Route{Platform: "telegram", ChatID: chat}
}
